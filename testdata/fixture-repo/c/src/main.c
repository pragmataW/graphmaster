#include <stdio.h>

#include "util.h"
#include "vec.h"

int main(void) {
    Vec2 a = {3.0, 4.0};
    Vec2 b = vec_scale(a, 2.0);
    Vec2 c = vec_add(a, b);
    log_msg("computed vectors");
    printf("%d\n", clamp_int((int)vec_len(c), 0, 100));
    return 0;
}
