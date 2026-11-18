## 1. Redirect URI per tenant

- [x] 1.1 Make the redirect URI from the id of the tenant that owns the connection.
- [x] 1.2 Serve the callback at `/callback/{tenant_id}`, and check that the tenant in the path owns the connection of the sign-in or the test.
- [x] 1.3 Change the tests that send an answer to another connection's path, so that they send it to another tenant's path.

## 2. Re-authentication as a request

- [x] 2.1 Remove the `auth_time` check at the callback and the reason `reauthentication_not_done`.
- [x] 2.2 Stop reading `auth_time` and the ticket's issue time.
- [x] 2.3 Remove the `auth_time` warning from the test sign-in page.
- [x] 2.4 Send no `prompt=login` and no `max_age=0` in a test sign-in.
