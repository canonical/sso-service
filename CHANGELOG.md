# Changelog

## 1.0.0 (2026-11-27)


### Features

* add the connection store, sealing and the clients ([6e0b75d](https://github.com/canonical/sso-service/commit/6e0b75df1b49c681e880db626efa2798aef27991))
* add the connection store, sealing and the clients ([#1](https://github.com/canonical/sso-service/issues/1)) ([7fedac0](https://github.com/canonical/sso-service/commit/7fedac0740f081499126a98840c26cfc10e70b9c))
* add the sign-in bridge and the management API ([c4e219f](https://github.com/canonical/sso-service/commit/c4e219fab7f93330c7f1c54e651c496d3735fd3b))
* add the sign-in bridge and the management API ([#2](https://github.com/canonical/sso-service/issues/2)) ([3445e9a](https://github.com/canonical/sso-service/commit/3445e9af80fe2d975ac383782646525a7ce93648))
* limit the binding cookies of a browser ([342ddb4](https://github.com/canonical/sso-service/commit/342ddb41bf002c9b8d7d9f997616c4b66fa9b330))


### Bug Fixes

* **authentication:** read the bearer scheme in any case, send WWW-Authenticate ([8da411a](https://github.com/canonical/sso-service/commit/8da411a0ccd1c52cf54ba6651b8dbeb836d3c4b8))
* **bridge:** fail a test sign-in on an unconfirmed address, keep log text printable ([e63702e](https://github.com/canonical/sso-service/commit/e63702e70da41e02b7d36782bb404f17ebb03407))
* **bridge:** log a sign-in the browser gave up on at debug ([661493e](https://github.com/canonical/sso-service/commit/661493e469a4444f186f6ba1e8c6928bcb59fff5))
* **cmd:** check the envelope key and the public URL first, bound the last export ([d69f377](https://github.com/canonical/sso-service/commit/d69f37723cb6f44613c9420f2786635de5230fff))
* **hydra:** read an expired challenge as expired, keep the challenge out of errors ([93f79f4](https://github.com/canonical/sso-service/commit/93f79f4f856dcd101748e14370a03273e8596778))
* **hydra:** refuse an accept or a reject answered with no redirect ([cc8788c](https://github.com/canonical/sso-service/commit/cc8788ccaba0bf31623541d36cfcff027886bd41))
* **idp:** close two gaps in the address check, classify failed reads by their cause ([74bdc13](https://github.com/canonical/sso-service/commit/74bdc13e9cd9fce013b81840c916b361f4a7bd44))
* **idp:** recognise a proxy named in another case or with a non-ASCII name ([de37c8b](https://github.com/canonical/sso-service/commit/de37c8bd07495c4f1969395c66c6a817cd9561f8))
* **idp:** record the guard's refusals on the request's span ([a633643](https://github.com/canonical/sso-service/commit/a633643edbdeabc34f292ad23e669fd3bb8f9c1f))
* **kratos:** keep the cause of a failed read, and no URL in any error ([4afdb00](https://github.com/canonical/sso-service/commit/4afdb00b647830bbc88f5f3a010d1a0bd516f56e))
* **kratos:** refuse an empty identifier, keep a decoded identity out of errors ([b237814](https://github.com/canonical/sso-service/commit/b23781425b0c8a4b43eb820727fddabd76819370))
* **logging:** keep the request URI with its query values redacted ([b45372c](https://github.com/canonical/sso-service/commit/b45372c6ba7f6533c99ac7f778cb6d11d0230bb4))
* **logging:** split an IPv6 host, skip a context value that is not a string ([2562752](https://github.com/canonical/sso-service/commit/25627521704606fe9e230bf007a3977f5fb4771a))
* read a cancelled call as a cancellation, not an outage ([a25880e](https://github.com/canonical/sso-service/commit/a25880e6f8927ea7ad1e530c57cac94ee09d4d7d))
* return configuration errors, accept warn and warning, log no query ([dfd5926](https://github.com/canonical/sso-service/commit/dfd5926ad5b0eb3c750b7d8876e578fbc861a401))
* send identity provider requests through the environment's proxy ([faf694f](https://github.com/canonical/sso-service/commit/faf694fa9eae156ac6f01ee09359255ff15acda5))
* **serve:** export the last spans at shutdown ([da7573d](https://github.com/canonical/sso-service/commit/da7573d2e20a35747f43ffc4a4936e43e445e422))
* **tenants:** read a cancelled call as a cancellation ([c71015c](https://github.com/canonical/sso-service/commit/c71015c872cebd89d9ed49408e49d1269600f799))
* **tenants:** wait for the service token only until the call's deadline ([7b55413](https://github.com/canonical/sso-service/commit/7b554131f9b102f6bb0511a64873738d447cd08e))
* **tracing:** flush on shutdown, honour the exporter's TLS settings, name spans after the route ([dff33f3](https://github.com/canonical/sso-service/commit/dff33f39d3d4eeb9d9fe9e1041796c59d03dccd3))
* **types:** read a link's connection id only in its canonical form ([3c3a831](https://github.com/canonical/sso-service/commit/3c3a8316c70f94966fdf64afd8cfd9c993aeafe6))
