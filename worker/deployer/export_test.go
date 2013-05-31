// Copyright 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package deployer

type fakeAddresser struct{}

func (*fakeAddresser) Addresses() []string {
	return []string{"s1:123", "s2:123"}, nil
}

func (*fakeAddresser) APIAddresses() []string {
	return []string{"a1:123", "a2:123"}, nil
}

func NewTestSimpleContext(deployerTag, initDir, dataDir, logDir, syslogConfigDir string) *SimpleContext {
	return &SimpleContext{
		addresser:       &fakeAddresser{},
		caCert:          []byte("test-cert"),
		deployerTag:     deployerTag,
		initDir:         initDir,
		dataDir:         dataDir,
		logDir:          logDir,
		syslogConfigDir: syslogConfigDir,
	}
}
