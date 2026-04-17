package main

import (
	"fmt"
	"github.com/in-toto/in-toto-golang/in_toto"
	"github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/common"
	slsa1 "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v1"
	slsav02 "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v0.2"
)

func main() {
	s := in_toto.Subject{Name: "foo", Digest: common.DigestSet{"sha256": "abc"}}
	m := common.ProvenanceMaterial{URI: "http://foo"}
	p1 := slsa1.ProvenancePredicate{}
	pv02 := slsav02.ProvenancePredicate{}
	fmt.Printf("%+v %+v %+v %+v\n", s, m, p1, pv02)
}
