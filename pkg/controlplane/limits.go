package controlplane

// MaxMessageSize caps daemon control-plane frame size to keep allocations bounded.
const MaxMessageSize = 1 * 1024 * 1024
