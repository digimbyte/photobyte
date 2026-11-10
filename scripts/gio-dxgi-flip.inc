// SPDX-License-Identifier: Unlicense OR MIT

// IDXGIFactory2 includes the inherited methods and IsWindowedStereoEnabled
// before CreateSwapChainForHwnd.
type photoByteFactory2 struct {
	Vtbl *struct {
		Inherited [15]uintptr
		CreateSwapChainForHwnd uintptr
	}
}

type photoByteSwapChainDesc struct {
	Width, Height, Format, Stereo uint32
	SampleDesc DXGI_SAMPLE_DESC
	Usage, BufferCount, Scaling, SwapEffect, AlphaMode, Flags uint32
}

func (d *Device) PhotoByteLimitFrameQueue() error {
	iid := GUID{0x77db970f, 0x6276, 0x48ba, 0xba, 0x28, 0x07, 0x01, 0x43, 0xb4, 0x39, 0x2c}
	dev, err := IUnknownQueryInterface(unsafe.Pointer(d), d.Vtbl.QueryInterface, &iid)
	if err != nil {
		return err
	}
	defer IUnknownRelease(unsafe.Pointer(dev), dev.Vtbl.Release)
	dxgi := (*struct {
		Vtbl *struct {
			Inherited [12]uintptr
			SetMaximumFrameLatency uintptr
		}
	})(unsafe.Pointer(dev))
	code, _, _ := syscall.SyscallN(dxgi.Vtbl.SetMaximumFrameLatency, uintptr(unsafe.Pointer(dxgi)), 1)
	if code != 0 {
		return ErrorCode{Name: "SetMaximumFrameLatency", Code: uint32(code)}
	}
	return nil
}

func PhotoByteCreateSwapChain(dev *Device, hwnd windows.Handle) (*IDXGISwapChain, error) {
	dxgiDev, err := IUnknownQueryInterface(unsafe.Pointer(dev), dev.Vtbl.QueryInterface, &IID_IDXGIDevice)
	if err != nil {
		return nil, err
	}
	defer IUnknownRelease(unsafe.Pointer(dxgiDev), dxgiDev.Vtbl.Release)
	adapter, err := (*IDXGIDevice)(unsafe.Pointer(dxgiDev)).GetAdapter()
	if err != nil {
		return nil, err
	}
	defer IUnknownRelease(unsafe.Pointer(adapter), adapter.Vtbl.Release)
	iidFactory2 := GUID{0x50c83a1c, 0xe072, 0x4c48, 0x87, 0xb0, 0x36, 0x30, 0xfa, 0x36, 0xa6, 0xd0}
	factory, err := (*IDXGIObject)(unsafe.Pointer(adapter)).GetParent(&iidFactory2)
	if err != nil {
		return nil, err
	}
	defer IUnknownRelease(unsafe.Pointer(factory), factory.Vtbl.Release)
	desc := photoByteSwapChainDesc{
		Format: DXGI_FORMAT_R8G8B8A8_UNORM, SampleDesc: DXGI_SAMPLE_DESC{Count: 1},
		Usage: DXGI_USAGE_RENDER_TARGET_OUTPUT, BufferCount: 2,
		SwapEffect: 4, // DXGI_SWAP_EFFECT_FLIP_DISCARD
		AlphaMode: 3, // DXGI_ALPHA_MODE_IGNORE
	}
	var chain *IDXGISwapChain
	vtable := (*photoByteFactory2)(unsafe.Pointer(factory)).Vtbl
	code, _, _ := syscall.SyscallN(vtable.CreateSwapChainForHwnd,
		uintptr(unsafe.Pointer(factory)), uintptr(unsafe.Pointer(dev)), uintptr(hwnd),
		uintptr(unsafe.Pointer(&desc)), 0, 0, uintptr(unsafe.Pointer(&chain)))
	if code != 0 {
		return nil, ErrorCode{Name: "CreateSwapChainForHwnd", Code: uint32(code)}
	}
	return chain, nil
}

func (d *Device) PhotoByteCreateSRGBRenderTargetView(resource *Resource) (*RenderTargetView, error) {
	// A flip chain uses UNORM buffers, with an sRGB view for the same
	// linear-to-sRGB conversion as the original Gio backbuffer.
	desc := struct { Format, Dimension uint32; View [3]uint32 }{
		Format: DXGI_FORMAT_R8G8B8A8_UNORM_SRGB,
		Dimension: 4, // D3D11_RTV_DIMENSION_TEXTURE2D
	}
	var target *RenderTargetView
	code, _, _ := syscall.SyscallN(d.Vtbl.CreateRenderTargetView, uintptr(unsafe.Pointer(d)),
		uintptr(unsafe.Pointer(resource)), uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&target)))
	if code != 0 {
		return nil, ErrorCode{Name: "CreateRenderTargetView", Code: uint32(code)}
	}
	return target, nil
}

func (c *DeviceContext) PhotoByteFlushReleasedBindings() {
	syscall.SyscallN(c.Vtbl.ClearState, uintptr(unsafe.Pointer(c)))
	syscall.SyscallN(c.Vtbl.Flush, uintptr(unsafe.Pointer(c)))
}
