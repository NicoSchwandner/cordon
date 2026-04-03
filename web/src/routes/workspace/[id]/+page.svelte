<script lang="ts">
	import StatusDot from '$lib/shared/components/StatusDot.svelte';
	import CreationProgress from '$lib/shared/components/CreationProgress.svelte';
	import AgentPanel from '$lib/security/AgentPanel.svelte';
	import { workspaceAction, deleteWorkspace, getWorkspace } from '$lib/shared/api/client';
	import { connectTerminalWS } from '$lib/shared/api/websocket';
	import { createTerminal } from '$lib/platform/terminal';

	let { data } = $props();

	let terminalEl: HTMLDivElement | undefined = $state();
	let creationDone = $state(false);
	const isCreating = $derived(!creationDone && (!data.workspace || data.workspace.status === 'creating'));
	const repos = $derived(data.workspace?.repos || []);
	const primaryRepo = $derived(repos.find(r => r.primary) || repos[0]);

	async function handleCreationReady() {
		// Refresh workspace data then switch to terminal view
		try {
			const ws = await getWorkspace(data.workspaceId);
			data.workspace = ws;
		} catch {
			// workspace may still be initializing, retry
		}
		creationDone = true;
	}

	$effect(() => {
		if (!terminalEl || isCreating) return;

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
				{#if repos.length > 0}
					<div class="flex items-center gap-1.5">
						{#each repos as repo}
							<span class="rounded-full bg-surface-raised px-2.5 py-1 text-xs font-mono text-foreground-secondary" title={repo.url}>
								{repo.url.split('/').pop()}{#if repo.branch}@{repo.branch}{/if}
								{#if repo.primary && repos.length > 1}
									<span class="ml-1 text-foreground-faint">(primary)</span>
								{/if}
							</span>
						{/each}
					</div>
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

	<!-- Main content: Progress or Terminal + Agent Panel -->
	{#if isCreating}
		<div class="flex-1 overflow-hidden rounded-xl border border-border bg-surface">
			<CreationProgress workspaceId={data.workspaceId} onready={handleCreationReady} />
		</div>
	{:else}
		<div class="grid flex-1 gap-4 lg:grid-cols-[1fr_320px]">
			<div class="overflow-hidden rounded-xl border border-border" style="background:#0f172a">
				<div bind:this={terminalEl} class="h-full min-h-[400px] w-full"></div>
			</div>

			<div class="overflow-hidden rounded-xl border border-border bg-surface">
				<AgentPanel workspaceId={data.workspaceId} />
			</div>
		</div>
	{/if}
</div>
