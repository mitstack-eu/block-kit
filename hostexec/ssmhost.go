package hostexec

import "errors"

// ErrNotSSM is the refusal SSMHost returns for any kind other than KindSSM.
var ErrNotSSM = errors.New("hostexec: host kind is not " + string(KindSSM))

// SSMHost builds the Host an operator drives over AWS Systems Manager.
// Target and region are left for Run to judge, and the refusal carries no
// part of the host it refused.
func SSMHost(kind Kind, target, credential, region string, env []string) (Host, error) {
	if kind != KindSSM {
		return Host{}, ErrNotSSM
	}
	return Host{Kind: KindSSM, Target: target, Credential: credential, Region: region, Env: env}, nil
}
