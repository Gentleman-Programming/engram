# Cloud inbox state isolation

The reserved `cloud:inbox` target retains local journal evidence. It is not a
transport queue, even if a retained row names a project that is now enrolled.

## Reads and refreshes

Reading a missing `cloud:<project>` state for an unenrolled project returns a
transient idle state without inserting a state row. Transport acknowledgement
and lifecycle refresh likewise do not create those foreign state rows.
Existing state and diagnostic history are not deleted or reinterpreted.

Explicit operational writes still persist failures, reasons, backoff, pending
state and successful-sync diagnostics, including before enrollment. A diagnostic
write does not enroll a project. Enrolled projects and the ordinary `cloud` and
`local:chunks` targets keep their existing behavior.

## Retention is not delivery

The inbox is excluded from pending push pages, sequence pagination, push
high-water marks and non-enrollment delivery counts. Range acknowledgement,
specific-sequence acknowledgement, skip-ack and irreparable-mutation repair
must not consume or quarantine its retained evidence. Its reserved lifecycle
and pending-row count remain observable through sync-state listing.

These guards do not claim, acknowledge, reassign or export historical inbox
rows. They do not implement alias-based capture routing, deletion repair,
legacy migration, delivery lanes or an inbox administration CLI. Those are
separate changes; existing capture and enrollment behavior is unchanged here.
