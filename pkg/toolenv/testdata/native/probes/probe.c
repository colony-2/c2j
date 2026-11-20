#include <stdio.h>
static int answer(int value) { return value * 6; }
int main(void) {
    if (answer(7) != 42) return 1;
    puts("native-c-ok");
    return 0;
}
