package topology

// ServiceDeployment is an observed service identity and its reporting device.
// DeviceID is the device primary key, never a topology node or Edge ID.
type ServiceDeployment struct {
	ServiceName      string
	ServiceNamespace string
	Environment      string
	DeviceID         uint64
}
