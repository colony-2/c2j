// Loaded only into subprocesses of the workspace regression test.
#include <errno.h>
#include <sys/types.h>
#include <unistd.h>

pid_t fork(void) {
    errno = EAGAIN;
    return -1;
}
