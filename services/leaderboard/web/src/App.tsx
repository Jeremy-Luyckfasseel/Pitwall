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
interface Snapshot {
  rows: RowView[];
}

// App subscribes to /events (Server-Sent Events) and re-renders the standings in
// place on every pushed snapshot — no polling, no reload (Q26.1, AC4). The board
// is read-only: it never sends anything back to the server.
export function App() {
  const [rows, setRows] = useState<RowView[]>([]);

  useEffect(() => {
    const source = new EventSource("/events");
    source.onmessage = (e) => {
      try {
        const snap: Snapshot = JSON.parse(e.data);
        setRows(snap.rows ?? []);
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
      </header>
      <ol className="standings" aria-label="Live race standings" aria-live="polite">
        {rows.length === 0 && (
          <li className="standings__empty">Waiting for the first lap…</li>
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
