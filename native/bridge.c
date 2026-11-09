#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <png.h>
#include <lcms2.h>
#include <webp/decode.h>
#include <webp/encode.h>

#define EXPORT __declspec(dllexport)

typedef struct {
    const unsigned char *data;
    size_t size;
    size_t offset;
} Input;

static void read_png(png_structp png, png_bytep dst, size_t size) {
    Input *input = png_get_io_ptr(png);
    if (size > input->size - input->offset) {
        png_error(png, "truncated PNG");
    }
    memcpy(dst, input->data + input->offset, size);
    input->offset += size;
}

EXPORT int dp_png_decode(const unsigned char *data, size_t size, unsigned char *pixels, uint32_t w, uint32_t h) {
    png_structp png = png_create_read_struct(PNG_LIBPNG_VER_STRING, NULL, NULL, NULL);
    if (!png) return 0;
    png_infop info = png_create_info_struct(png);
    if (!info || setjmp(png_jmpbuf(png))) {
        png_destroy_read_struct(&png, &info, NULL);
        return 0;
    }

    Input input = {data, size, 0};
    png_set_user_limits(png, 16384, 16384);
    png_set_chunk_malloc_max(png, 4U << 20);
    png_set_read_fn(png, &input, read_png);
    png_read_info(png, info);
    if (png_get_image_width(png, info) != w || png_get_image_height(png, info) != h) {
        png_error(png, "PNG dimensions changed");
    }
    png_byte type = png_get_color_type(png, info);
    int alpha = (type & PNG_COLOR_MASK_ALPHA) || png_get_valid(png, info, PNG_INFO_tRNS);
    png_set_expand_16(png);
    if (type == PNG_COLOR_TYPE_GRAY || type == PNG_COLOR_TYPE_GRAY_ALPHA) {
        png_set_gray_to_rgb(png);
    }
    if (!alpha) png_set_add_alpha(png, 65535, PNG_FILLER_AFTER);
    int passes = png_set_interlace_handling(png);
    png_read_update_info(png, info);
    if (png_get_rowbytes(png, info) != (size_t)w * 8) {
        png_error(png, "PNG did not expand to RGBA16");
    }
    for (int pass = 0; pass < passes; ++pass) {
        for (uint32_t y = 0; y < h; ++y) {
            png_read_row(png, pixels + (size_t)y * w * 8, NULL);
        }
    }
    png_read_end(png, NULL);
    png_destroy_read_struct(&png, &info, NULL);
    return 1;
}

static int write_output(HANDLE handle, const unsigned char *data, size_t size) {
    while (size != 0) {
        DWORD written = 0;
        DWORD batch = size < (1U << 20) ? (DWORD)size : 1U << 20;
        if (!WriteFile(handle, data, batch, &written, NULL) || written == 0) return 0;
        data += written;
        size -= written;
    }
    return 1;
}

static void write_png(png_structp png, png_bytep data, size_t size) {
    if (!write_output(png_get_io_ptr(png), data, size)) {
        png_error(png, "could not write PNG output");
    }
}

EXPORT void *dp_png_begin(HANDLE out, uint32_t w, uint32_t h, int depth, int space) {
    png_structp png = png_create_write_struct(PNG_LIBPNG_VER_STRING, NULL, NULL, NULL);
    if (!png) return NULL;
    png_infop info = png_create_info_struct(png);
    if (!info || setjmp(png_jmpbuf(png))) {
        png_destroy_write_struct(&png, &info);
        return NULL;
    }
    png_set_write_fn(png, out, write_png, NULL);
    png_set_compression_level(png, 2);
    png_set_IHDR(png, info, w, h, depth, PNG_COLOR_TYPE_RGBA, PNG_INTERLACE_NONE,
                 PNG_COMPRESSION_TYPE_DEFAULT, PNG_FILTER_TYPE_DEFAULT);
    if (space == 1) png_set_sRGB(png, info, PNG_sRGB_INTENT_PERCEPTUAL);
    if (space == 2) png_set_cICP(png, info, 12, 13, 0, 1);
    png_write_info(png, info);
    png_destroy_info_struct(png, &info);
    return png;
}

EXPORT int dp_png_row(png_structp png, png_bytep data) {
    if (setjmp(png_jmpbuf(png))) return 0;
    png_write_row(png, data);
    return 1;
}

EXPORT int dp_png_end(png_structp png, int complete) {
    if (setjmp(png_jmpbuf(png))) {
        png_destroy_write_struct(&png, NULL);
        return 0;
    }
    if (complete) png_write_end(png, NULL);
    png_destroy_write_struct(&png, NULL);
    return 1;
}

EXPORT int dp_icc_srgb(const void *bytes, uint32_t size, unsigned char *pixels, uint32_t count) {
    cmsHPROFILE src = cmsOpenProfileFromMem(bytes, size);
    if (!src) return 0;
    cmsHPROFILE dst = cmsCreate_sRGBProfile();
    cmsHTRANSFORM transform = NULL;
    int result = 0;
    if (dst && cmsGetColorSpace(src) == cmsSigRgbData) {
        transform = cmsCreateTransform(src, TYPE_RGBA_16_SE, dst, TYPE_RGBA_16_SE,
                                       INTENT_RELATIVE_COLORIMETRIC, cmsFLAGS_COPY_ALPHA);
        if (transform) {
            cmsDoTransform(transform, pixels, pixels, count);
            result = 1;
        }
    } else if (dst && cmsGetColorSpace(src) == cmsSigGrayData) {
        transform = cmsCreateTransform(src, TYPE_GRAY_16_SE, dst, TYPE_RGB_16_SE,
                                       INTENT_RELATIVE_COLORIMETRIC, 0);
        if (transform) {
            uint16_t gray[512];
            uint16_t rgb[3 * 512];
            while (count > 0) {
                uint32_t n = count < 512 ? count : 512;
                for (uint32_t i = 0; i < n; ++i) memcpy(&gray[i], pixels + i * 8, 2);
                cmsDoTransform(transform, gray, rgb, n);
                for (uint32_t i = 0; i < n; ++i) memcpy(pixels + i * 8, &rgb[i * 3], 6);
                pixels += n * 8;
                count -= n;
            }
            result = 1;
        }
    }
    if (transform) cmsDeleteTransform(transform);
    if (dst) cmsCloseProfile(dst);
    cmsCloseProfile(src);
    return result;
}

EXPORT int dp_webp_decode(const uint8_t *data, size_t size, uint8_t *dst, int w, int h) {
    int actual_w, actual_h;
    if (!WebPGetInfo(data, size, &actual_w, &actual_h) || actual_w != w || actual_h != h) return 0;
    return WebPDecodeRGBAInto(data, size, dst, (size_t)w * h * 4, w * 4) != NULL;
}

EXPORT int dp_webp_write(HANDLE out, const uint8_t *data, int w, int h) {
    WebPConfig config;
    WebPPicture pic;
    WebPMemoryWriter memory;
    if (!WebPConfigInit(&config) || !WebPPictureInit(&pic)) return 0;
    if (!WebPConfigLosslessPreset(&config, 4)) return 0;
    config.exact = 1;
    pic.use_argb = 1;
    pic.width = w;
    pic.height = h;
    WebPMemoryWriterInit(&memory);
    pic.writer = WebPMemoryWrite;
    pic.custom_ptr = &memory;
    int ok = WebPPictureImportRGBA(&pic, data, w * 4) && WebPEncode(&config, &pic);
    if (ok) ok = write_output(out, memory.mem, memory.size);
    WebPPictureFree(&pic);
    WebPMemoryWriterClear(&memory);
    return ok;
}
