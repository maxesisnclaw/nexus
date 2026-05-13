module github.com/maxesisnclaw/nexus-demo

go 1.26.1

require (
	github.com/maxesisnclaw/nexus v0.0.0
	github.com/vmihailenco/msgpack/v5 v5.4.1
)

require (
	github.com/flynn/noise v1.1.0 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	golang.org/x/crypto v0.0.0-20210322153248-0c34fe9e7dc2 // indirect
	golang.org/x/sys v0.30.0 // indirect
)

// Always build the demo against the in-tree nexus source.
// From services/go/ up to the repo root is four hops:
//   services/go -> services -> microservices-compose -> examples -> repo root
replace github.com/maxesisnclaw/nexus => ../../../..
