## MODIFIED Requirements

### Requirement: Shutdown
The system SHALL stop on `SIGINT` or `SIGTERM`, or when one of its listeners fails. To stop, the system SHALL do these steps in order:

1. Accept no more requests on either listener.
2. Let the requests in flight finish for up to 20 seconds.
3. Cut whatever is still running.
4. Export the spans that it still holds, for up to 3 seconds (see `observability`).
5. Exit.

The export SHALL have its own bound of 3 seconds. Thus the export gets its time also when the requests in flight used all of the 20 seconds.

#### Scenario: A request in flight at shutdown
- **WHEN** a signal tells the service to stop while the service handles a request
- **THEN** the service answers that request if it finishes within 20 seconds
- **AND** the service accepts no new request

#### Scenario: The last spans after a long drain
- **WHEN** a signal tells the service to stop
- **AND** a request in flight does not finish within 20 seconds
- **THEN** the service cuts that request
- **AND** the service exports the spans that it still holds, for up to 3 seconds
- **AND** then the service exits
