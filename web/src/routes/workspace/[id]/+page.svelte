<script lang="ts">
	import StatusDot from '$lib/shared/components/StatusDot.svelte';
	import AgentPanel from '$lib/security/AgentPanel.svelte';
	import { workspaceAction, deleteWorkspace } from '$lib/shared/api/client';
	import { connectTerminalWS } from '$lib/shared/api/websocket';
	import { createTerminal } from '$lib/platform/terminal';

	let { data } = $props();

	let terminalEl: HTMLDivElement | undefined = $state();

	$effect(() => {
		if (!terminalEl) return;

		const { terminal, fit, dispose } = createTerminal(terminalEl);
		const ws = connectTerminalWS(data.workspaceId);

		ws.onData((d) => terminal.write(d));
		terminal.onData((d) => ws.send(d));
		terminal.onResize(({ cols, rows }) => ws.resize(cols, rows));

		return () => {
			ws.close();
			dispose();
		};
	});

	async function handleAction(action: 'suspend' | 'resume') {
		try {
			await workspaceAction(data.workspaceId, action);
			window.location.reload();
		} catch (e) {
			alert(e instanceof Error ? e.message : 'Action failed');
		}
	}

	async function handleDelete() {
		if (!confirm('Destroy this workspace? This cannot be undone.')) return;
		try {
			await deleteWorkspace(data.workspaceId);
			window.location.href = '/';
		} catch (e) {
			alert(e instanceof Error ? e.message : 'Delete failed');
		}
	}
</script>

<div class="flex h-[calc(100vh-4rem)] flex-col">
	<!-- Header -->
	<div class="mb-4 flex items-center justify-between">
		<div class="flex items-center gap-3">
			<a href="/" class="text-foreground-faint transition-colors hover:text-foreground">&larr;</a>
			{#if data.workspace}
				<h1 class="text-xl font-semibold tracking-tight text-foreground">{data.workspace.name}</h1>
				<StatusDot status={data.workspace.status} />
				{#if data.workspace.repo}
					<span class="rounded-full bg-surface-raised px-2.5 py-1 text-xs font-mono text-foreground-secondary">
						{data.workspace.branch || 'main'}
					</span>
				{/if}
			{:else}
				<h1 class="text-xl font-semibold tracking-tight text-foreground">Workspace {data.workspaceId}</h1>
			{/if}
		</div>
		<div class="flex gap-2">
			{#if data.workspace?.status === 'running'}
				<button
					onclick={() => handleAction('suspend')}
					class="rounded-lg border border-border-input px-3 py-1.5 text-sm text-foreground-secondary transition-colors hover:bg-hover-subtle"
				>Suspend</button>
			{:else if data.workspace?.status === 'suspended'}
				<button
					onclick={() => handleAction('resume')}
					class="rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-on-primary transition-colors hover:bg-primary-hover"
				>Resume</button>
			{/if}
			<button
				onclick={handleDelete}
				class="rounded-lg bg-danger-solid px-3 py-1.5 text-sm font-medium text-on-danger transition-colors hover:bg-danger-solid-hover"
			>Destroy</button>
		</div>
	</div>

	<!-- Main content: Terminal + Agent Panel -->
	<div class="grid flex-1 gap-4 lg:grid-cols-[1fr_320px]">
		<div class="overflow-hidden rounded-xl border border-border" style="background:#0f172a">
			<div bind:this={terminalEl} class="h-full min-h-[400px] w-full"></div>
		</div>

		<div class="overflow-hidden rounded-xl border border-border bg-surface">
			<AgentPanel workspaceId={data.workspaceId} />
		</div>
	</div>
</div>
