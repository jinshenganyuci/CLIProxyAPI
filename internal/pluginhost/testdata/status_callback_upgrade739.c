#include <stdint.h>
#include <stdlib.h>
#include <string.h>

typedef struct { void *ptr; size_t len; } cliproxy_buffer;
typedef struct {
  uint32_t abi_version;
  void *host_ctx;
  int (*call)(void *, const char *, const uint8_t *, size_t, cliproxy_buffer *);
  void (*free_buffer)(void *, size_t);
} cliproxy_host_api;
typedef struct {
  uint32_t abi_version;
  int (*call)(const char *, const uint8_t *, size_t, cliproxy_buffer *);
  void (*free_buffer)(void *, size_t);
  void (*shutdown)(void);
} cliproxy_plugin_api;
static const cliproxy_host_api *host_api;
static int call_host(const char *method, const uint8_t *request, size_t length, cliproxy_buffer *out) {
  cliproxy_buffer from_host = {0};
  int result = host_api->call(host_api->host_ctx, method, request, length, &from_host);
  if (result != 0) return result;
  out->len = from_host.len;
  out->ptr = malloc(out->len);
  if (out->ptr == NULL) {
    host_api->free_buffer(from_host.ptr, from_host.len);
    return 1;
  }
  memcpy(out->ptr, from_host.ptr, out->len);
  host_api->free_buffer(from_host.ptr, from_host.len);
  return 0;
}
static void free_plugin(void *ptr, size_t length) { (void)length; free(ptr); }
int cliproxy_plugin_init(const cliproxy_host_api *host, cliproxy_plugin_api *plugin) {
  host_api = host;
  plugin->abi_version = host->abi_version;
  plugin->call = call_host;
  plugin->free_buffer = free_plugin;
  plugin->shutdown = NULL;
  return 0;
}
