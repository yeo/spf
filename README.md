
# blitiri.com.ar/go/spf

[![GoDoc](https://godoc.org/blitiri.com.ar/go/spf?status.svg)](https://pkg.go.dev/blitiri.com.ar/go/spf)
[![Build Status](https://gitlab.com/albertito/spf/badges/master/pipeline.svg)](https://gitlab.com/albertito/spf/-/commits/master)
[![Go Report Card](https://goreportcard.com/badge/github.com/albertito/spf)](https://goreportcard.com/report/github.com/albertito/spf)
[![Coverage Status](https://coveralls.io/repos/github/albertito/spf/badge.svg?branch=next)](https://coveralls.io/github/albertito/spf)

[spf](https://godoc.org/blitiri.com.ar/go/spf) is an open source
implementation of the Sender Policy Framework (SPF) in Go.

It is used by the [chasquid](https://blitiri.com.ar/p/chasquid/) and
[maddy](https://foxcpp.dev/maddy) SMTP servers.


## Example

The API is quite simple: it has only one main function to perform the SPF
check, similar to the one suggested in the
[RFC](https://tools.ietf.org/html/rfc7208).

```go
// Check if `sender` is authorized to send from the given `ip`. The `domain`
// is used if the sender doesn't have one.
result, err := spf.CheckHostWithSender(ip, domain, sender)
if result == spf.Fail {
	// Not authorized to use the domain.
}
```

See the [package documentation](https://pkg.go.dev/blitiri.com.ar/go/spf) for
more details.


## Status

The API should be considered stable. Major version changes will be announced
to the mailing list (details below).


## Contact

If you have any questions, comments or patches please send them to the mailing
list, `chasquid@googlegroups.com`.

To subscribe, send an email to `chasquid+subscribe@googlegroups.com`.

You can also browse the
[archives](https://groups.google.com/forum/#!forum/chasquid).

