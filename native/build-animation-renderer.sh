#!/bin/sh
# Run from this directory with the Alpine build-base, pkgconf, imagemagick-dev and rlottie-dev packages.
set -eu
cc -std=c11 -O2 -Wall -Wextra -Werror animation-renderer.c \
    $(pkg-config --cflags --libs MagickWand) -lrlottie -lm -o loom-animation
