## 1. Proxy

- [x] 1.1 Read the proxy settings from the environment once, when the client is made, with `golang.org/x/net/http/httpproxy`.
- [x] 1.2 Before a request goes to a proxy, resolve the host of the request and refuse a non-public address.
- [x] 1.3 Dial the address of the proxy without the address check.
- [x] 1.4 Add a test for a public host and a private host through a proxy.
