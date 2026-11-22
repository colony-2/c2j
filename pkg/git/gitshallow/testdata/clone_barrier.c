// Test-only interposition: pause a real Git object access without changing its
// arguments, return value or errno. The Go test performs real maintenance.
#define _GNU_SOURCE
#include <dlfcn.h>
#include <fcntl.h>
#include <limits.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/syscall.h>
#include <unistd.h>

static void barrier(const char *path, const char *operation) {
    const char *object = getenv("C2J_CLONE_OBJECT");
    const char *ready = getenv("C2J_CLONE_READY");
    const char *release = getenv("C2J_CLONE_RELEASE");
    char resolved[PATH_MAX];
    if (!object || !ready || !release || !realpath(path, resolved) || strcmp(object, resolved)) return;
    int fd = syscall(SYS_openat, AT_FDCWD, ready, O_WRONLY | O_CREAT | O_EXCL, 0600);
    if (fd < 0) return;
    dprintf(fd, "%s pid=%ld source=%s\n", operation, (long)getpid(), resolved);
    close(fd);
    for (int i = 0; i < 2000; ++i) {
        if (access(release, F_OK) == 0) return;
        usleep(10000);
    }
    // A harness failure must not silently become a passing clone.
    _exit(99);
}

int link(const char *source, const char *target) {
    int (*real_link)(const char *, const char *) = dlsym(RTLD_NEXT, "link");
    barrier(source, "link");
    return real_link(source, target);
}

#define WRAP_OPEN(name) \
int name(const char *path, int flags, ...) { \
    int (*real_open)(const char *, int, ...) = dlsym(RTLD_NEXT, #name); \
    mode_t mode = 0; \
    if ((flags & O_CREAT) || ((flags & O_TMPFILE) == O_TMPFILE)) { \
        va_list args; va_start(args, flags); mode = va_arg(args, mode_t); va_end(args); \
    } \
    barrier(path, #name); \
    return real_open(path, flags, mode); \
}
WRAP_OPEN(open)
WRAP_OPEN(open64)

int __open64_2(const char *path, int flags) {
    int (*real_open)(const char *, int) = dlsym(RTLD_NEXT, "__open64_2");
    barrier(path, "__open64_2");
    return real_open(path, flags);
}
