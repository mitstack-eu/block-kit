package hostexec

import "errors"

var ErrNotSSM = errors.New("hostexec: host kind is not " + string(KindSSM))

func SSMHost(kind Kind, target, credential, region string, env []string) (Host, error) {
	if kind != KindSSM {
		return Host{}, ErrNotSSM
	}
	return Host{Kind: KindSSM, Target: target, Credential: credential, Region: region, Env: env}, nil
}
