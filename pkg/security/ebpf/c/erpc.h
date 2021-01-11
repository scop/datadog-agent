#ifndef _ERPC_H
#define _ERPC_H

#include "filters.h"

#define RPC_CMD 0xdeadc001

enum erpc_op {
    UNKNOW_OP,
    DISCARD_INODE_OP,
    DISCARD_PID_OP
};

int __attribute__((always_inline)) handle_discard(void *data, u64 *event_type, u64 *timeout) {
    u64 value;

    bpf_probe_read(&value, sizeof(value), data);
    *event_type = value;

    bpf_probe_read(&value, sizeof(value), data + sizeof(value));
    *timeout = value;

    return 2*sizeof(value);
}

int __attribute__((always_inline)) handle_discard_inode(void *data) {
    u64 event_type, timeout;

    data += handle_discard(data, &event_type, &timeout);

    u64 inode;
    bpf_probe_read(&inode, sizeof(inode), data);
    data += sizeof(inode);

    u32 mount_id;
    bpf_probe_read(&mount_id, sizeof(mount_id), data);

    return discard_inode(event_type, mount_id, inode, timeout);
}

int __attribute__((always_inline)) handle_discard_pid(void *data) {
    u64 event_type, timeout;

    data += handle_discard(data, &event_type, &timeout);

    u32 pid;
    bpf_probe_read(&pid, sizeof(pid), data);

    return discard_pid(event_type, pid, timeout);
}

int __attribute__((always_inline)) is_eprc_request(struct pt_regs *ctx) {
    u64 fd, pid;

    LOAD_CONSTANT("erpc_fd", fd);
    LOAD_CONSTANT("erpc_pid", pid);

    u32 vfs_fd = PT_REGS_PARM2(ctx);
    if (!vfs_fd || (u64)vfs_fd != fd) {
        return 0;
    }

    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tgid = pid_tgid >> 32;

    if ((u64)tgid != pid) {
        return 0;
    }

    u32 cmd = PT_REGS_PARM3(ctx);
    if (cmd != RPC_CMD) {
        return 0;
    }

    return 1;
}

int __attribute__((always_inline)) handle_erpc_request(struct pt_regs *ctx) {
    void *req = (void *)PT_REGS_PARM4(ctx);

    u8 op;
    bpf_probe_read(&op, sizeof(op), req);

   void *data = req + sizeof(op);

    switch (op) {
        case DISCARD_INODE_OP:
            return handle_discard_inode(data);
        case DISCARD_PID_OP:
            return handle_discard_pid(data);
    }

    return 0;
}

#endif