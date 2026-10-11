#include <stdio.h>

#include "util.h"

void log_msg(const char *msg) {
    fprintf(stderr, "[fixture] %s\n", msg);
}

int clamp_int(int v, int lo, int hi) {
    if (v < lo) {
        return lo;
    }
    return v > hi ? hi : v;
}
