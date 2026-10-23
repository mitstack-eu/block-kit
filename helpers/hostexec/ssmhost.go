package hostexec

import "errors"

// ErrNotSSM is the refusal SSMHost returns for any kind other than KindSSM.
var ErrNotSSM = errors.New("hostexec: host kind is not " + string(KindSSM))

// SSMHost builds an SSM Host, leaving target and region for Run to judge, and its refusal carries no part of the host.
func SSMHost(kind Kind, target, credential, region string, env []string) (Host, error) {
	if kind != KindSSM {
		return Host{}, ErrNotSSM
	}
	return Host{Kind: KindSSM, Target: target, Credential: credential, Region: region, Env: env}, nil
}
