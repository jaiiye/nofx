package hook

import "nofx/logger"

type IpResult struct {
	Err error
	IP  string
}

func (r *IpResult) Error() error {
	return r.Err
}

func (r *IpResult) GetResult() string {
	if r.Err != nil {
		logger.Warnf("⚠️ Error executing GetIP: %v", r.Err)
	}
	return r.IP
}
