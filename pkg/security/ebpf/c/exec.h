#ifndef _EXEC_H_
#define _EXEC_H_

#include <linux/tty.h>

#include "filters.h"
#include "syscalls.h"
#include "container.h"

#define MAX_PERF_STR_BUFF_LEN 64
#define MAX_STR_BUFF_LEN (1 << 15)
#define MAX_ARRAY_ELEMENT 64
#define MAX_ARRAY_ELEMENT_SIZE 4096

struct perf_str_buffer_t {
    u32 id;
    u32 truncated;
    char value[MAX_PERF_STR_BUFF_LEN];
};

struct str_buffer_t {
    char value[MAX_STR_BUFF_LEN];
};

struct bpf_map_def SEC("maps/args_envs_cache") args_envs_cache = {
    .type = BPF_MAP_TYPE_LRU_HASH,
    .key_size = sizeof(u32),
    .value_size = sizeof(struct str_buffer_t),
    .max_entries = 255,
    .pinning = 0,
    .namespace = "",
};

#define ARGS_BUFFER_KEY 0
#define ENVS_BUFFER_KEY 1

struct bpf_map_def SEC("maps/str_buffers") str_buffers = {
    .type = BPF_MAP_TYPE_PERCPU_ARRAY,
    .key_size = sizeof(u32),
    .value_size = sizeof(struct str_buffer_t),
    .max_entries = 2,
    .pinning = 0,
    .namespace = "",
};

struct exec_event_t {
    struct kevent_t event;
    struct process_context_t process;
    struct proc_cache_t proc_entry;
    struct pid_cache_t pid_entry;
    struct perf_str_buffer_t args;
    struct perf_str_buffer_t envs;
};

struct exit_event_t {
    struct kevent_t event;
    struct process_context_t process;
    struct container_context_t container;
};

struct _tracepoint_sched_process_fork
{
    unsigned short common_type;
    unsigned char common_flags;
    unsigned char common_preempt_count;
    int common_pid;

    char parent_comm[16];
    pid_t parent_pid;
    char child_comm[16];
    pid_t child_pid;
};

void __attribute__((always_inline)) copy_proc_cache(struct proc_cache_t *dst, struct proc_cache_t *src) {
    dst->executable = src->executable;
    fill_container_context(src, &dst->container);
    return;
}

static __attribute__((always_inline)) u32 copy_tty_name(char dst[TTY_NAME_LEN], char src[TTY_NAME_LEN]) {
    if (src[0] == 0) {
        return 0;
    }

#pragma unroll
    for (int i = 0; i < TTY_NAME_LEN; i++) {
        dst[i] = src[i];
    }
    return TTY_NAME_LEN;
}

void __attribute__((always_inline)) parse_str_array(struct str_array_ref_t *array_ref, const char **data, int buff_key) {
    u32 id = bpf_get_prandom_u32();
    array_ref->id = id;

    u32 key = buff_key;
    struct str_buffer_t *buff = bpf_map_lookup_elem(&str_buffers, &key);
    if (!buff) {
        return;
    }

    int i = 0, a = 1, n = 0, offset = 0;

    const char *str;
    bpf_probe_read(&str, sizeof(str), (void *)&data[a]);

#pragma unroll
    for (i = 0; i < MAX_ARRAY_ELEMENT; i++) {
        n = bpf_probe_read_str(&(buff->value[(offset + sizeof(n)) & (MAX_STR_BUFF_LEN - MAX_ARRAY_ELEMENT_SIZE - 1)]), MAX_ARRAY_ELEMENT_SIZE, (void *)str);
        if (n > 0) {
            n--; // remove trailing 0
            bpf_probe_read(&(buff->value[offset&(MAX_STR_BUFF_LEN - MAX_ARRAY_ELEMENT_SIZE - 1)]), sizeof(n), &n);
            bpf_probe_read(&str, sizeof(str), (void *)&data[++a]);
            offset += n + sizeof(n);
        } else {
            break;
        }
    }

    array_ref->truncated = i == MAX_ARRAY_ELEMENT;
    bpf_map_update_elem(&args_envs_cache, &id, buff, BPF_ANY);
}

int __attribute__((always_inline)) trace__sys_execveat(const char **argv, const char **env) {
    struct syscall_cache_t syscall = {
        .type = SYSCALL_EXEC,
    };
    parse_str_array(&syscall.exec.args, argv, ARGS_BUFFER_KEY);
    parse_str_array(&syscall.exec.envs, env, ENVS_BUFFER_KEY);

    cache_syscall(&syscall);
    return 0;
}

SYSCALL_KPROBE3(execve, const char *, filename, const char **, argv, const char **, env) {
    return trace__sys_execveat(argv, env);
}

SYSCALL_KPROBE4(execveat, int, fd, const char *, filename, const char **, argv, const char **, env) {
    return trace__sys_execveat(argv, env);
}

int __attribute__((always_inline)) handle_exec_event(struct pt_regs *ctx, struct syscall_cache_t *syscall) {
    if (syscall->exec.is_parsed) {
        return 0;
    }
    syscall->exec.is_parsed = 1;

    struct file *file = (struct file *)PT_REGS_PARM1(ctx);
    struct inode *inode = (struct inode *)PT_REGS_PARM2(ctx);
    struct path *path = &file->f_path;

    syscall->exec.dentry = get_file_dentry(file);
    syscall->exec.path_key = get_inode_key_path(inode, &file->f_path);
    syscall->exec.path_key.path_id = get_path_id(0);

    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tgid = pid_tgid >> 32;

    struct proc_cache_t entry = {
        .executable = {
            .inode = syscall->exec.path_key.ino,
            .overlay_numlower = get_overlay_numlower(get_path_dentry(path)),
            .mount_id = get_path_mount_id(path),
            .path_id = syscall->exec.path_key.path_id,
        },
        .container = {},
        .exec_timestamp = bpf_ktime_get_ns(),
    };
    bpf_get_current_comm(&entry.comm, sizeof(entry.comm));

    // cache dentry
    resolve_dentry(syscall->exec.dentry, syscall->exec.path_key, 0);

    u32 cookie = bpf_get_prandom_u32();
    // insert new proc cache entry
    bpf_map_update_elem(&proc_cache, &cookie, &entry, BPF_ANY);

    // select the previous cookie entry in cache of the current process
    // (this entry was created by the fork of the current process)
    struct pid_cache_t *fork_entry = (struct pid_cache_t *) bpf_map_lookup_elem(&pid_cache, &tgid);
    if (fork_entry) {
        // Fetch the parent proc cache entry
        u32 parent_cookie = fork_entry->cookie;
        struct proc_cache_t *parent_entry = bpf_map_lookup_elem(&proc_cache, &parent_cookie);
        if (parent_entry) {
            // inherit the parent container context
            fill_container_context(parent_entry, &entry.container);
        }
        // update pid <-> cookie mapping
        fork_entry->cookie = cookie;
    } else {
        struct pid_cache_t new_pid_entry = {
            .cookie = cookie,
        };
        bpf_map_update_elem(&pid_cache, &tgid, &new_pid_entry, BPF_ANY);
    }

    return 0;
}

#define DO_FORK_STRUCT_INPUT 1

int __attribute__((always_inline)) handle_do_fork(struct pt_regs *ctx) {
    u64 input;
    LOAD_CONSTANT("do_fork_input", input);

    if (input == DO_FORK_STRUCT_INPUT) {
        void *args = (void *)PT_REGS_PARM1(ctx);
        int exit_signal;
        bpf_probe_read(&exit_signal, sizeof(int), (void *)args + 32);

        // Only insert an entry if this is a thread
        if (exit_signal == SIGCHLD) {
            return 0;
        }
    } else {
        u64 flags = (u64)PT_REGS_PARM1(ctx);

        if ((flags & SIGCHLD) == SIGCHLD) {
            return 0;
        }
    }

    struct syscall_cache_t syscall = {
        .type = SYSCALL_FORK,
        .clone = {
            .is_thread = 1,
        }
    };
    cache_syscall(&syscall);

    return 0;
}

SEC("kprobe/kernel_clone")
int kprobe_kernel_clone(struct pt_regs *ctx) {
    return handle_do_fork(ctx);
}

SEC("kprobe/do_fork")
int krpobe_do_fork(struct pt_regs *ctx) {
    return handle_do_fork(ctx);
}

SEC("kprobe/_do_fork")
int kprobe__do_fork(struct pt_regs *ctx) {
    return handle_do_fork(ctx);
}

SEC("tracepoint/sched/sched_process_fork")
int sched_process_fork(struct _tracepoint_sched_process_fork *args) {
    // check if this is a thread first
    struct syscall_cache_t *syscall = pop_syscall(SYSCALL_FORK);
    if (syscall) {
        return 0;
    }

    u32 pid = 0;
    u32 ppid = 0;
    bpf_probe_read(&pid, sizeof(pid), &args->child_pid);
    u64 ts = bpf_ktime_get_ns();

    struct exec_event_t event = {
        .pid_entry.fork_timestamp = ts,
    };
    bpf_get_current_comm(&event.proc_entry.comm, sizeof(event.proc_entry.comm));
    fill_process_context(&event.process);

    // the `parent_pid` entry of `sched_process_fork` might point to the TID (and not PID) of the parent. Since we
    // only work with PID, we can't use the TID. This is why we use the PID generated by the eBPF context instead.
    ppid = event.process.pid;
    event.pid_entry.ppid = ppid;
    // sched::sched_process_fork is triggered from the parent process, update the pid / tid to the child value
    event.process.pid = pid;
    event.process.tid = pid;
    event.pid_entry.uid = event.process.uid;
    event.pid_entry.gid = event.process.gid;

    struct pid_cache_t *parent_pid_entry = (struct pid_cache_t *) bpf_map_lookup_elem(&pid_cache, &ppid);
    if (parent_pid_entry) {
        // ensures pid and ppid point to the same cookie
        event.pid_entry.cookie = parent_pid_entry->cookie;

        // fetch the parent proc cache entry
        struct proc_cache_t *parent_proc_entry = bpf_map_lookup_elem(&proc_cache, &event.pid_entry.cookie);
        if (parent_proc_entry) {

            // copy parent proc cache entry data
            event.proc_entry.executable.inode = parent_proc_entry->executable.inode;
            event.proc_entry.executable.overlay_numlower = parent_proc_entry->executable.overlay_numlower;
            event.proc_entry.executable.mount_id = parent_proc_entry->executable.mount_id;
            event.proc_entry.executable.path_id = parent_proc_entry->executable.path_id;
            event.proc_entry.exec_timestamp = parent_proc_entry->exec_timestamp;
            copy_tty_name(event.proc_entry.tty_name, parent_proc_entry->tty_name);

            // fetch container context
            fill_container_context(parent_proc_entry, &event.proc_entry.container);
        }
    }

    // insert the pid cache entry for the new process
    bpf_map_update_elem(&pid_cache, &pid, &event.pid_entry, BPF_ANY);

    // send the entry to maintain userspace cache
    send_event(args, EVENT_FORK, event);

    return 0;
}

SEC("kprobe/do_exit")
int kprobe_do_exit(struct pt_regs *ctx) {
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tgid = pid_tgid >> 32;
    u32 pid = pid_tgid;

    if (tgid == pid) {
        if (!is_flushing_discarders()) {
            bpf_map_delete_elem(&pid_discarders, &tgid);
        }

        // update exit time
        struct pid_cache_t *pid_entry = (struct pid_cache_t *) bpf_map_lookup_elem(&pid_cache, &tgid);
        if (pid_entry) {
            pid_entry->exit_timestamp = bpf_ktime_get_ns();
        }

        // send the entry to maintain userspace cache
        struct exit_event_t event = {};
        struct proc_cache_t *cache_entry = fill_process_context(&event.process);
        fill_container_context(cache_entry, &event.container);

        send_event(ctx, EVENT_EXIT, event);
    }

    return 0;
}

SEC("kprobe/exit_itimers")
int kprobe_exit_itimers(struct pt_regs *ctx) {
    struct signal_struct *signal = (struct signal_struct *)PT_REGS_PARM1(ctx);

    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tgid = pid_tgid >> 32;

    struct proc_cache_t *entry = get_proc_cache(tgid);
    if (entry) {
        struct tty_struct *tty;
        bpf_probe_read(&tty, sizeof(tty), &signal->tty);
        bpf_probe_read_str(entry->tty_name, TTY_NAME_LEN, tty->name);
    }

    return 0;
}

void __attribute__((always_inline)) fill_args_envs(struct exec_event_t *event, struct syscall_cache_t *syscall) {
    if (syscall->exec.args.id) {
        struct str_buffer_t *buff = bpf_map_lookup_elem(&args_envs_cache, &syscall->exec.args.id);
        if (buff) {
            bpf_probe_read(&event->args.value, MAX_PERF_STR_BUFF_LEN, buff->value);
            event->args.id = syscall->exec.args.id;
            event->args.truncated = syscall->exec.args.truncated;
        }
    }

    if (syscall->exec.envs.id) {
        struct str_buffer_t *buff = bpf_map_lookup_elem(&args_envs_cache, &syscall->exec.envs.id);
        if (buff) {
            bpf_probe_read(&event->envs.value, MAX_PERF_STR_BUFF_LEN, buff->value);
            event->envs.id = syscall->exec.envs.id;
            event->envs.truncated = syscall->exec.envs.truncated;
        }
    }
}

SEC("kprobe/security_bprm_committed_creds")
int kprobe_security_bprm_committed_creds(struct pt_regs *ctx) {
    struct syscall_cache_t *syscall = pop_syscall(SYSCALL_EXEC);
    if (!syscall) {
        return 0;
    }

    // check if this is a thread first
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tgid = pid_tgid >> 32;

    struct pid_cache_t *pid_entry = (struct pid_cache_t *) bpf_map_lookup_elem(&pid_cache, &tgid);
    if (pid_entry) {
        u32 cookie = pid_entry->cookie;
        struct proc_cache_t *proc_entry = bpf_map_lookup_elem(&proc_cache, &cookie);
        if (proc_entry) {
            struct exec_event_t event = {
                .proc_entry.executable = {
                    .inode = proc_entry->executable.inode,
                    .overlay_numlower = proc_entry->executable.overlay_numlower,
                    .mount_id = proc_entry->executable.mount_id,
                    .path_id = proc_entry->executable.path_id,
                },
                .proc_entry.container = {},
                .proc_entry.exec_timestamp = proc_entry->exec_timestamp,
                .pid_entry.cookie = pid_entry->cookie,
                .pid_entry.ppid = pid_entry->ppid,
                .pid_entry.fork_timestamp = pid_entry->fork_timestamp,
            };
            bpf_get_current_comm(&event.proc_entry.comm, sizeof(event.proc_entry.comm));
            copy_tty_name(event.proc_entry.tty_name, proc_entry->tty_name);

            fill_process_context(&event.process);
            fill_container_context(proc_entry, &event.proc_entry.container);
            fill_args_envs(&event, syscall);

            // send the entry to maintain userspace cache
            send_event(ctx, EVENT_EXEC, event);
        }
    }

    return 0;
}
#endif
