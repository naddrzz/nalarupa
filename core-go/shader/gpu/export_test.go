package gpu

// HardwareVulkan reports whether shader fills run on a GPU through Vulkan, where the Rust core's
// NoContraction SPIR-V (shader.rs `strict_spirv`) keeps float hashes consistent and wgpu-native cannot.
func HardwareVulkan() bool {
	mu.Lock()
	defer mu.Unlock()
	d, err := current()
	return err == nil && !d.cpu && d.backend == "Vulkan"
}
