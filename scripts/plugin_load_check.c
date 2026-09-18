#include <dlfcn.h>
#include <stdio.h>

static const char *required_symbols[] = {
    "cliproxy_plugin_init",
    "cliproxyPluginCall",
    "cliproxyPluginFree",
    "cliproxyPluginShutdown",
    NULL,
};

int main(int argc, char **argv) {
    if (argc != 2) {
        fprintf(stderr, "usage: %s path/to/upstream-monitor.so\n", argv[0]);
        return 2;
    }

    void *handle = dlopen(argv[1], RTLD_NOW | RTLD_LOCAL);
    if (handle == NULL) {
        fprintf(stderr, "dlopen failed: %s\n", dlerror());
        return 1;
    }

    for (const char **symbol = required_symbols; *symbol != NULL; symbol++) {
        dlerror();
        (void)dlsym(handle, *symbol);
        const char *error = dlerror();
        if (error != NULL) {
            fprintf(stderr, "dlsym(%s) failed: %s\n", *symbol, error);
            return 1;
        }
    }

    puts("plugin load check passed");
    return 0;
}
