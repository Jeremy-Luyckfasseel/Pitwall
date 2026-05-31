# Pitwall — Project Brief

## What is Pitwall?

Pitwall is a platform for karting tracks that connects all operational systems into one integrated whole. A karting track runs many things at once: drivers show up and get registered, sessions start and laps get recorded, the bar processes orders, the back office sends invoices, and the TV screens show a live leaderboard. Right now all of these are separate, disconnected pieces.

Pitwall connects them. Every event that happens in one part of the system — a driver arriving, a lap being set, a session ending — is automatically known by every other part that cares about it. No manual data entry between systems, no race conditions where the billing system doesn't know a session is over yet.

The key architectural principle is that none of these services talk to each other directly. They all publish and listen to a shared message bus in the middle. That means if one service goes down, the rest keep running independently.

## Core Principles

- **Loosely coupled:** Every service is independent. If billing goes down, drivers can still scan in, laps still get timed, and the leaderboard still updates.
- **Event-driven:** Every meaningful action in the system produces an event. Other services react to those events rather than being called directly.
- **Fault tolerant:** No service going offline should block another. The system degrades gracefully, never halts entirely.
- **No "computer says no":** Every edge case must have a handled outcome. The system should always offer a path forward.

## The Services

### 1. Timing
Handles both the physical scanning of drivers and all lap time calculations. A built-in simulator can fake scans during development so real hardware is never required to work on the system. This is the primary data source for the entire platform.

Responsibilities:
- Detect and validate driver entry via QR code or transponder at the entry gate
- Record a lap event every time a driver crosses the start-finish line
- Calculate lap times from consecutive scan events
- Track best lap per session and all-time personal record per driver
- Publish events when a personal record is broken
- Publish a session summary event when a session ends
- Handle scanner hardware going offline gracefully
- Simulate scan events for development and testing

### 2. Frontend
The public-facing website where drivers can register an account, browse available sessions, book a slot, and view their personal history and lap records after a session.

Responsibilities:
- Driver registration and profile management
- Session browsing and booking
- Personal lap history and personal record display
- Confirmation flow after booking

### 3. Booking
Manages the session schedule: which heats are running, how many spots are available, and what the current track programme looks like. Keeps the schedule consistent when sessions run late or get cancelled.

Responsibilities:
- Maintain a schedule of sessions and heats
- Track available capacity per session
- Handle delays and reschedules cascading to downstream sessions
- Publish events when a session starts or ends

### 4. Driver
Stores driver profiles and their full history across all sessions. Acts as the source of truth for who a driver is and their complete lap history.

Responsibilities:
- Create and update driver profiles
- Store lap history per driver across sessions
- Provide driver data to other services on request

### 5. Billing
Handles all financial transactions: session charges and bar orders. Generates invoices for companies and immediate receipts for individual drivers. Must handle the case where an individual driver requests an invoice even though they are not linked to a company.

Responsibilities:
- Open a billing tab when a driver checks in
- Record bar orders against a driver's tab
- Generate a receipt or invoice when a session ends
- Handle edge case: private person requesting a formal invoice

### 6. Mailing
Sends automated emails triggered by events in the system. Never sends emails on its own initiative — always reacts to something that happened elsewhere.

Responsibilities:
- Booking confirmation after a session is reserved
- Session summary with lap times after a session ends
- Personal best alert when a driver breaks their record
- Invoice or receipt delivery after billing closes a tab

### 7. Leaderboard
A live display of current standings and lap times during an active session. Updates in real time as new laps come in. Visible on screens at the track independently of the main frontend.

Responsibilities:
- Display live standings ordered by best lap time
- Update immediately when a new lap event arrives
- Reset automatically when a new session starts
- Show session status (active / finished)

## What Every Service Must Deliver

Regardless of what a service does, every one is responsible for the same baseline:

- Runs in a Docker container
- Publishes relevant events to the message bus
- Listens and reacts to relevant events from other services
- Validates all incoming messages and logs errors on bad data
- Exposes a `/health` endpoint
- Sends a heartbeat signal every second so the control room knows it is alive
- All code lives in Git with at minimum a `main`, `dev`, and `prod` branch
- Automated deployment pipeline: a push to the right branch deploys automatically

## Monitoring / Control Room

A separate control room watches every service. It does not sit on the message bus — instead it checks each service's health endpoint directly, so it can detect a service that has gone completely silent on the bus. When something goes down, an alert fires immediately.

The control room also provides a dashboard with:
- Live status (online / offline) for every service
- Statistics: active drivers, sessions today, laps recorded
- Alert history

## Edge Cases to Handle

The system must handle at minimum the following edge cases without breaking:

- A session runs late, requiring downstream sessions in the schedule to shift automatically
- A private driver (not linked to a company) requests a formal invoice after a session
- A service restarts mid-session and needs to catch up on missed events
- Conflicting driver data arrives from two services at the same time
- The scanner goes offline mid-session — laps recorded before the outage must not be lost
- A driver tries to book a session that is already full

Think through the "sad paths" for each service and define what the correct behaviour is. The answer should never be an error with no way forward.

## Build Order (Suggested)

1. Get the message bus and control room running first — nothing else can be validated without them
2. Build Timing (start with the simulator) as the first end-to-end slice
3. Add Leaderboard — now you can see data flowing visually
4. Add Driver — now driver identity is real
5. Add Booking — now sessions have structure
6. Add Frontend — now external users can interact
7. Add Billing — now the financial layer is live
8. Add Mailing — now the communication layer closes the loop
9. Harden: add validation, error logging, sad-path handling, and deploy pipelines across all services
