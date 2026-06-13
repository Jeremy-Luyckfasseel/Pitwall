import { useEffect, useState } from "react";

// Wire shape pushed by the Go service over SSE (matches internal/web.RowView).
interface RowView {
  position: number;
  masterId: string;
  displayName: string;
  lapTimeMs: number;
  lapTime: string;
  isFastest: boolean;
}
// Session status block (FR45): "active" | "finished"; null before any session
// has ever been seen (matches internal/web.SessionView).
interface SessionView {
  sessionId: string;
  status: string;
}
interface Snapshot {
  session: SessionView | null;
  rows: RowView[];
  // Bus-connection state (Story 1.10): when the leaderboard's broker connection
  // is down the board freezes on last-known standings and flags stale —
  // honest degradation (FR47, C1), never faked-live.
  stale?: boolean;
  connection?: string;
}

// App subscribes to /events (Server-Sent Events) and re-renders the standings in
// place on every pushed snapshot — no polling, no reload (Q26.1, AC4). The board
// is read-only: it never sends anything back to the server. A session.started
// auto-resets the board (the snapshot simply carries the new session's rows) and
// the status-pill flips between active and finished (FR43/FR45) — status is
// always paired with its text label, never color alone.
export function App() {
  const [session, setSession] = useState<SessionView | null>(null);
  const [rows, setRows] = useState<RowView[]>([]);
  const [stale, setStale] = useState(false);

  useEffect(() => {
    const source = new EventSource("/events");
    source.onmessage = (e) => {
      try {
        const snap: Snapshot = JSON.parse(e.data);
        setSession(snap.session ?? null);
        setRows(snap.rows ?? []);
        setStale(snap.stale === true);
      } catch {
        // Ignore a malformed frame; the next snapshot supersedes it.
      }
    };
    return () => source.close();
  }, []);

  return (
    <main className="board">
      <header className="board__header">
        <h1 className="board__title">Live Standings</h1>
        {stale ? (
          // Bus down: freeze on last-known, flag reconnecting (calm warning,
          // never faked-live). Announced politely for assistive tech.
          <span className="status-pill mono status-pill--reconnecting" role="status" aria-live="polite">
            Showing last-known · reconnecting…
          </span>
        ) : (
          session && (
            <span
              className={
                "status-pill mono" +
                (session.status === "finished"
                  ? " status-pill--finished"
                  : " status-pill--active")
              }
            >
              {session.status}
            </span>
          )
        )}
      </header>
      <ol className="standings" aria-label="Live race standings" aria-live="polite">
        {rows.length === 0 && (
          <li className="standings__empty">
            {session?.status === "finished"
              ? "No laps were recorded in this session."
              : "Waiting for the first lap…"}
          </li>
        )}
        {rows.map((r) => (
          <li
            key={r.masterId}
            className={
              "leaderboard-row" + (r.isFastest ? " leaderboard-row--fastest" : "")
            }
          >
            <span className="leaderboard-row__pos mono">{r.position}</span>
            <span className="leaderboard-row__driver">
              {r.displayName}
              {r.isFastest && <span className="fl-badge">FL</span>}
            </span>
            <span className="leaderboard-row__time mono">{r.lapTime}</span>
          </li>
        ))}
      </ol>
    </main>
  );
}
