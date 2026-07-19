//go:build !windows

// 非 Windows stub：所有方法返回 ErrUnsupportedPlatform。
// 这使得 manager（Linux）可以引用 install 包的符号而不编译失败。

package install

// unsupportedSecretStore 是非 Windows 平台的 SecretStore stub。
type unsupportedSecretStore struct{}

// NewSecretStore 在非 Windows 平台返回 stub 实现。
func NewSecretStore(_ string) SecretStore {
	return unsupportedSecretStore{}
}

func (unsupportedSecretStore) Install(_ []byte) error { return ErrUnsupportedPlatform }
func (unsupportedSecretStore) Rotate(_ []byte) error  { return ErrUnsupportedPlatform }
func (unsupportedSecretStore) Remove() error           { return ErrUnsupportedPlatform }

// unsupportedServiceController 是非 Windows 平台的 ServiceController stub。
type unsupportedServiceController struct{}

// NewServiceController 在非 Windows 平台返回 stub 实现。
func NewServiceController(_ string) ServiceController {
	return unsupportedServiceController{}
}

func (unsupportedServiceController) Create(_ string) error                  { return ErrUnsupportedPlatform }
func (unsupportedServiceController) ConfigureRecovery() error              { return ErrUnsupportedPlatform }
func (unsupportedServiceController) ConfigureDefenderExclusion() error     { return ErrUnsupportedPlatform }
func (unsupportedServiceController) Start() error                          { return ErrUnsupportedPlatform }
func (unsupportedServiceController) Stop() error                           { return ErrUnsupportedPlatform }
func (unsupportedServiceController) Delete() error                         { return ErrUnsupportedPlatform }

// unsupportedEnvWriter 是非 Windows 平台的 EnvWriter stub。
type unsupportedEnvWriter struct{}

// NewEnvWriter 在非 Windows 平台返回 stub 实现。
func NewEnvWriter(_ string) EnvWriter {
	return unsupportedEnvWriter{}
}

func (unsupportedEnvWriter) Write(_ []string) error { return ErrUnsupportedPlatform }
