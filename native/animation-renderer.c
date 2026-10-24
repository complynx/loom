/* Isolated raster output for Telegram/Lottie shapes and MNG animations. */
#include <MagickWand/MagickWand.h>
#include <rlottie_capi.h>
#include <math.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define MAX_PIXELS 16000000
#define MAX_FRAMES 1000
#define MAX_INPUT (20 * 1024 * 1024)

static int valid_size(size_t w, size_t h) {
    return w > 0 && h > 0 && w <= MAX_PIXELS / h;
}

static int write_lottie(Lottie_Animation *animation, size_t frame,
                        size_t width, size_t height, const char *output) {
    uint32_t *pixels = calloc(width * height, sizeof(uint32_t));
    unsigned char *rgba = calloc(width * height, 4);
    if (!pixels || !rgba) { free(pixels); free(rgba); return 1; }
    lottie_animation_render(animation, frame, pixels, width, height, width * 4);
    /* rlottie returns premultiplied ARGB; PNG uses straight RGBA. */
    for (size_t i = 0; i < width * height; ++i) {
        unsigned int alpha = pixels[i] >> 24;
        rgba[4*i+3] = alpha;
        if (alpha) {
            rgba[4*i] = ((pixels[i] >> 16) & 255) * 255 / alpha;
            rgba[4*i+1] = ((pixels[i] >> 8) & 255) * 255 / alpha;
            rgba[4*i+2] = (pixels[i] & 255) * 255 / alpha;
        }
    }
    MagickWand *wand = NewMagickWand();
    int ok = MagickConstituteImage(wand, width, height, "RGBA", CharPixel, rgba)
          && MagickSetImageFormat(wand, "PNG") && MagickWriteImage(wand, output);
    DestroyMagickWand(wand);
    free(pixels); free(rgba);
    return !ok;
}

static int lottie(const char *input, const char *output, double at, size_t edge) {
    FILE *file = fopen(input, "rb");
    if (!file) return 1;
    char *data = calloc(MAX_INPUT + 1, 1);
    if (!data) { fclose(file); return 1; }
    size_t bytes = fread(data, 1, MAX_INPUT + 1, file);
    int failed = ferror(file) || bytes > MAX_INPUT;
    fclose(file);
    if (failed) { free(data); return 1; }
    Lottie_Animation *animation = lottie_animation_from_data(data, "loom", "");
    free(data);
    if (!animation) return 1;
    size_t width = 0, height = 0;
    lottie_animation_get_size(animation, &width, &height);
    size_t frames = lottie_animation_get_totalframe(animation);
    double rate = lottie_animation_get_framerate(animation);
    double duration = (double)frames / rate;
    int result = 1;
    /* rlottie may include a terminal frame beyond Lottie's exclusive out point. */
    if (!valid_size(width, height) || !frames || frames > MAX_FRAMES+1 ||
        !isfinite(rate) || rate <= 0 || !isfinite(duration) || duration <= 0) goto done;
    if (output && at < duration) {
        double scale = fmin((double)edge/width, (double)edge/height);
        width = (size_t)fmax(1, width*scale);
        height = (size_t)fmax(1, height*scale);
        size_t frame = (size_t)floor(at*rate);
        if (frame >= frames) frame = frames-1;
        result = write_lottie(animation, frame, width, height, output);
    }
done:
    lottie_animation_destroy(animation);
    return result;
}

static double frame_delay(MagickWand *wand) {
    size_t ticks = MagickGetImageTicksPerSecond(wand);
    double delay = (double)MagickGetImageDelay(wand) / (ticks ? ticks : 100);
    return delay > 0 ? delay : 0.01;
}

static int mng(const char *input, const char *output, double at, size_t edge) {
    MagickWand *wand = NewMagickWand();
    char path[4096];
    if (snprintf(path, sizeof(path), "MNG:%s", input) >= (int)sizeof(path)) {
        DestroyMagickWand(wand); return 1;
    }
    int result = 1;
    if (!MagickReadImage(wand, path)) goto done;
    size_t frames = MagickGetNumberImages(wand);
    if (!frames || frames > MAX_FRAMES) goto done;
    double duration = 0;
    MagickResetIterator(wand);
    while (MagickNextImage(wand)) {
        if (!valid_size(MagickGetImageWidth(wand), MagickGetImageHeight(wand))) goto done;
        duration += frame_delay(wand);
    }
    MagickSetFirstIterator(wand);
    if (!output) {
        printf("{\"width\":%zu,\"height\":%zu,\"frames\":%zu,\"duration\":%.9f}\n",
               MagickGetImageWidth(wand), MagickGetImageHeight(wand), frames, duration);
        result = 0;
    } else if (at < duration) {
        MagickWand *composed = MagickCoalesceImages(wand);
        if (!composed) goto done;
        double end = 0;
        MagickResetIterator(composed);
        while (MagickNextImage(composed)) {
            end += frame_delay(composed);
            if (at >= end) continue;
            size_t width = MagickGetImageWidth(composed), height = MagickGetImageHeight(composed);
            if (!valid_size(width,height)) break;
            double scale = fmin((double)edge/width, (double)edge/height);
            width = (size_t)fmax(1,width*scale); height = (size_t)fmax(1,height*scale);
            result = !(MagickThumbnailImage(composed,width,height) &&
                       MagickSetImageFormat(composed,"PNG") && MagickWriteImage(composed,output));
            break;
        }
        DestroyMagickWand(composed);
    }
done:
    DestroyMagickWand(wand);
    return result;
}

/* Arguments are supplied only by the worker. Output files stay in its job directory. */
int main(int argc, char **argv) {
    if (argc != 3 && argc != 6) return 2;
    double at = 0;
    size_t edge = 1280;
    if (argc == 6) {
        char *end = NULL;
        at = strtod(argv[4], &end);
        if (!end || *end || !isfinite(at) || at < 0) return 2;
        unsigned long value = strtoul(argv[5], &end, 10);
        if (!end || *end || value < 1 || value > 2048) return 2;
        edge = value;
    }
    MagickWandGenesis();
    MagickSetResourceLimit(ThreadResource, 1);
    MagickSetResourceLimit(MemoryResource, 128*1024*1024);
    MagickSetResourceLimit(MapResource, 0);
    MagickSetResourceLimit(DiskResource, 32*1024*1024);
    MagickSetResourceLimit(WidthResource, MAX_PIXELS);
    MagickSetResourceLimit(HeightResource, MAX_PIXELS);
    MagickSetResourceLimit(ListLengthResource, MAX_FRAMES);
    MagickSetResourceLimit(TimeResource, 25);
    const char *output = argc == 6 ? argv[3] : NULL;
    int result = 2;
    if (!strcmp(argv[1], "lottie")) result = lottie(argv[2], output, at, edge);
    if (!strcmp(argv[1], "mng")) result = mng(argv[2], output, at, edge);
    MagickWandTerminus();
    return result;
}
