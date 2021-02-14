#ifndef _SPAN_H
#define _SPAN_H

#define GOLANG 1
#define PYTHON 2

struct coroutine_ctx_t {
    u8 type;
    char data[223];
};

struct bpf_map_def SEC("maps/coroutine_ctx") coroutine_ctx = {
    .type = BPF_MAP_TYPE_LRU_HASH,
    .key_size = sizeof(u32),
    .value_size = sizeof(struct coroutine_ctx_t),
    .max_entries = 4096,
    .pinning = 0,
    .namespace = "",
};

struct bpf_map_def SEC("maps/coroutine_ids") coroutine_ids = {
    .type = BPF_MAP_TYPE_LRU_HASH,
    .key_size = sizeof(u64),
    .value_size = sizeof(u64),
    .max_entries = 4096,
    .pinning = 0,
    .namespace = "",
};

struct span_key_t {
    u64 coroutine_id;
    u32 id;
    u32 padding;
};

struct span_t {
    u64 span_id;
    u64 trace_id;
};

struct bpf_map_def SEC("maps/span_ids") span_ids = {
    .type = BPF_MAP_TYPE_LRU_HASH,
    .key_size = sizeof(struct span_key_t),
    .value_size = sizeof(struct span_t),
    .max_entries = 4096,
    .pinning = 0,
    .namespace = "",
};

static __attribute__((always_inline)) struct span_t *get_current_span() {
    u64 id = bpf_get_current_pid_tgid();
    u32 pid = id >> 32;

    // select coroutine context
    struct coroutine_ctx_t *co_ctx = bpf_map_lookup_elem(&coroutine_ctx, &pid);
    if (co_ctx == NULL) {
        return NULL;
    }

    // select current goroutine id
    struct span_key_t key = {};
    u64 *coroutine_id = bpf_map_lookup_elem(&coroutine_ids, &id);
    if (coroutine_id != NULL) {
        key.coroutine_id = *coroutine_id;
    }

    // select span based on the type of coroutine
    switch (co_ctx->type) {
        case (GOLANG): {
            // for golang, use the pid of the process
            key.id = pid;
            break;
        }
        case (PYTHON): {
            key.id = id;
            break;
        }
    }

    return bpf_map_lookup_elem(&span_ids, &key);
}

struct stack_trace_signature_t {
    u64 nodes_sig1[3];
    u64 nodes_sig2[3];
};

struct bpf_map_def SEC("maps/stack_trace_signatures") stack_trace_signatures = {
    .type = BPF_MAP_TYPE_LRU_HASH,
    .key_size = sizeof(u32),
    .value_size = sizeof(struct stack_trace_signature_t),
    .max_entries = 4096,
    .pinning = 0,
    .namespace = "",
};

int __attribute__((always_inline)) check_stack_trace_signature(struct pt_regs *ctx, int pid) {
    // build current signature
    struct stack_trace_signature_t active_sig = {};
    bpf_get_stack(ctx, active_sig.nodes_sig1, 3 * sizeof(u64), BPF_F_USER_STACK);
    bpf_printk("node1:%lu node2:%lu node3:%lu\n", active_sig.nodes_sig1[0], active_sig.nodes_sig1[1], active_sig.nodes_sig1[2]);

    // check with existing signature
    struct stack_trace_signature_t *sig = bpf_map_lookup_elem(&stack_trace_signatures, &pid);
    if (sig != NULL) {

        // check if this is a span creation request
        if (sig->nodes_sig1[0] != active_sig.nodes_sig1[0] || sig->nodes_sig1[1] != active_sig.nodes_sig1[1] || sig->nodes_sig1[2] != active_sig.nodes_sig1[2]) {
            // check if the second signature was set
            if (sig->nodes_sig2[0] == 0) {
                // accept the active signature as the second valid signature
                sig->nodes_sig2[0] = active_sig.nodes_sig1[0];
                sig->nodes_sig2[1] = active_sig.nodes_sig1[1];
                sig->nodes_sig2[2] = active_sig.nodes_sig1[2];
                return 1;
            }

            // invalid signature, check if this is a span finish request
            if (sig->nodes_sig2[0] != active_sig.nodes_sig1[0] || sig->nodes_sig2[1] != active_sig.nodes_sig1[1] || sig->nodes_sig2[2] != active_sig.nodes_sig1[2]) {
                // invalid signature
                return 0;
            }
        }
        return 1;
    }

    // no signature yet, save the active one
    bpf_map_update_elem(&stack_trace_signatures, &pid, &active_sig, BPF_ANY);
    return 1;
}

struct bpf_map_def SEC("maps/secret_tokens") secret_tokens = {
    .type = BPF_MAP_TYPE_LRU_HASH,
    .key_size = sizeof(u32),
    .value_size = sizeof(u64),
    .max_entries = 4096,
    .pinning = 0,
    .namespace = "",
};

int __attribute__((always_inline)) check_secret_token(int pid, u64 token) {
    bpf_printk("provided_token:%lu\n", token);
    // fetch the secret token of the current pid
    u64 *secret_token = bpf_map_lookup_elem(&secret_tokens, &pid);
    if (secret_token != NULL) {
        if (*secret_token != token) {
            // invalid token
            return 0;
        }
        return 1;
    }

    // no secret token yet, save the current one
    bpf_map_update_elem(&secret_tokens, &pid, &token, BPF_ANY);
    return 1;
}

int __attribute__((always_inline)) handle_span_id(struct pt_regs *ctx, void *data) {
    u64 id = bpf_get_current_pid_tgid();
    u32 pid = id >> 32;
    struct span_key_t key = {};
    struct span_t span = {};
    struct coroutine_ctx_t co_ctx = {};
    u64 secret_token;

    // parse the provided data (span id, trace id, coroutine id, language specific data)
    bpf_probe_read(&secret_token, sizeof(secret_token), data);
    bpf_probe_read(&span.span_id, sizeof(span.span_id), data + 8);
    bpf_probe_read(&span.trace_id, sizeof(span.trace_id), data + 16);
    bpf_probe_read(&key.coroutine_id, sizeof(key.coroutine_id), data + 24);
    bpf_probe_read(&co_ctx.type, sizeof(co_ctx.type), data + 32);
    bpf_probe_read(&co_ctx.data, sizeof(co_ctx.data), data + 33);

    // set key id based on coroutine type
    switch (co_ctx.type) {
        case (GOLANG): {
            key.id = pid;

            // check stack trace signature
            if (check_stack_trace_signature(ctx, pid) == 0) {
                // invalid signature, ignore the span
                bpf_printk("invalid stack trace signature !\n");
                return 0;
            }
            bpf_printk("valid stack trace signature :)\n");
            break;
        }
        case (PYTHON): {
            key.id = id;

            // check secret token
            if (check_secret_token(pid, secret_token) == 0) {
                // invalid token, ignore the span
                bpf_printk("invalid secret token !\n");
                return 0;
            }
            bpf_printk("valid secret token :)\n");
            break;
        }
    }

    // save span id and co_data context for future use
    bpf_map_update_elem(&span_ids, &key, &span, BPF_ANY);
    bpf_map_update_elem(&coroutine_ctx, &pid, &co_ctx, BPF_ANY);

    // update thread id <-> coroutine id mapping
    bpf_map_update_elem(&coroutine_ids, &id, &key.coroutine_id, BPF_ANY);
    return 0;
}

#endif
