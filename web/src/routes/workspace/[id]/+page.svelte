<script lang="ts">
	import StatusDot from '$lib/shared/components/StatusDot.svelte';
	import TimeRemaining from '$lib/shared/components/TimeRemaining.svelte';
	import CreationProgress from '$lib/shared/components/CreationProgress.svelte';
	import AgentPanel from '$lib/security/AgentPanel.svelte';
	import MessageBanner from '$lib/shared/components/MessageBanner.svelte';
	import { workspaceAction, deleteWorkspace, getWorkspace, activateRepos, connectCreationSSE, extendWorkspace } from '$lib/shared/api/client';
	import type { WorkspaceResponse } from '$lib/shared/api/types';
	import { connectTerminalWS } from '$lib/shared/api/websocket';
	import { createTerminal } from '$lib/platform/terminal';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';

	let { data } = $props();

	// Use navigation state as fallback when workspace data isn't loaded yet
	// (container doesn't exist during creation, so GET returns 404)
	const navState = page.state as Record<string, unknown> | undefined;
	const initialWorkspace = data.workspace ?? (navState?.workspace as WorkspaceResponse | undefined) ?? null;

	// Redirect to dashboard if workspace genuinely doesn't exist (not mid-creation)
	if (data.notFound && !navState?.workspace) {
		goto('/', { replaceState: true });
	}

	let workspace = $state<WorkspaceResponse | null>(initialWorkspace);
	let terminalEl: HTMLDivElement | undefined = $state();
	let creationDone = $state(false);
	const isCreating = $derived(!creationDone && (!workspace || workspace.status === 'creating'));
	const isInvestigation = $derived(workspace?.mode === 'investigation');
	const investigation = $derived(workspace?.investigation);
	const repos = $derived(workspace?.repos || []);
	const primaryRepo = $derived(repos.find(r => r.primary) || repos[0]);

	// Investigation state
	let repoFilter = $state('');
	let activatingRepo = $state('');
	let activationProgress = $state('');
	let actionError = $state('');
	const filteredShallowRepos = $derived(() => {
		if (!investigation?.shallow_repos) return [];
		const q = repoFilter.toLowerCase();
		if (!q) return investigation.shallow_repos;
		return investigation.shallow_repos.filter(r => r.toLowerCase().includes(q));
	});

	function repoShortName(url: string): string {
		const parts = url.replace(/\.git$/, '').split('/');
		return parts[parts.length - 1] || url;
	}

	function isActivated(url: string): boolean {
		return investigation?.activated_repos?.includes(url) ?? false;
	}

	function spawnedWorkspaceForRepo(url: string): import('$lib/shared/api/types').WorkspaceResponse | undefined {
		const short = repoShortName(url);
		return data.spawnedWorkspaces?.find(ws => ws.name === short);
	}

	async function handleActivate(repoURL: string) {
		activatingRepo = repoURL;
		activationProgress = 'Starting activation...';
		try {
			await activateRepos(data.workspaceId, [repoURL]);
			connectCreationSSE(data.workspaceId, (evt) => {
				activationProgress = evt.message;
				if (evt.done) {
					activatingRepo = '';
					activationProgress = '';
					getWorkspace(data.workspaceId).then(ws => { workspace = ws; }).catch(() => {});
				}
			});
		} catch (e) {
			actionError = e instanceof Error ? e.message : 'Activation failed';
			activatingRepo = '';
			activationProgress = '';
		}
	}

	async function handleCreationReady() {
		// Refresh workspace data then switch to terminal view
		try {
			const ws = await getWorkspace(data.workspaceId);
			workspace = ws;
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

	// Poll workspace status to detect external changes (reaper suspend, TTL expiry)
	$effect(() => {
		if (isCreating) return;
		const interval = setInterval(async () => {
			try {
				workspace = await getWorkspace(data.workspaceId);
			} catch {
				// workspace may have been destroyed
			}
		}, 10_000);
		return () => clearInterval(interval);
	});

	async function handleAction(action: 'suspend' | 'resume') {
		actionError = '';
		try {
			await workspaceAction(data.workspaceId, action);
			workspace = await getWorkspace(data.workspaceId);
		} catch (e) {
			actionError = e instanceof Error ? e.message : 'Action failed';
		}
	}

	let extending = $state(false);

	async function handleExtend(duration: string) {
		extending = true;
		actionError = '';
		try {
			const result = await extendWorkspace(data.workspaceId, duration);
			if (workspace) {
				workspace = { ...workspace, expires_at: result.expires_at };
			}
		} catch (e) {
			actionError = e instanceof Error ? e.message : 'Extend failed';
		} finally {
			extending = false;
		}
	}

	async function handleDelete() {
		if (!confirm('Destroy this workspace? This cannot be undone.')) return;
		actionError = '';
		try {
			await deleteWorkspace(data.workspaceId);
			window.location.href = '/';
		} catch (e) {
			actionError = e instanceof Error ? e.message : 'Delete failed';
		}
	}
</script>

<div class="flex h-[calc(100vh-4rem)] flex-col overflow-hidden">
	<!-- Header -->
	<div class="mb-4 flex items-start justify-between gap-4">
		<div class="min-w-0 flex-1">
			<div class="flex items-center gap-3">
				<a href="/" class="text-foreground-faint transition-colors hover:text-foreground">&larr;</a>
				{#if workspace}
					<h1 class="text-xl font-semibold tracking-tight text-foreground">{workspace.name}</h1>
					<StatusDot status={workspace.status} />
				{:else}
					<h1 class="text-xl font-semibold tracking-tight text-foreground">Workspace {data.workspaceId}</h1>
				{/if}
			</div>
			{#if workspace}
				{#if isInvestigation && investigation}
					<div class="mt-1.5 flex items-center gap-2">
						<span class="rounded-full bg-info-badge-bg px-2.5 py-1 text-xs text-info-text">{investigation.catalog_org}</span>
						<span class="text-xs text-foreground-faint">{investigation.shallow_repos?.length || 0} repos</span>
					</div>
				{:else if repos.length > 0}
					<div class="mt-1.5 flex flex-wrap items-center gap-1.5">
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
			{/if}
		</div>
		<div class="flex shrink-0 items-center gap-3">
			{#if workspace?.status === 'running' || workspace?.status === 'suspended'}
				<div class="flex items-center gap-1.5 rounded-lg border border-border px-3 py-1.5">
					<span class="text-xs"><TimeRemaining expiresAt={workspace.expires_at} label="TTL" /></span>
					{#if workspace.status === 'running'}
						<button
							onclick={() => handleExtend('2h')}
							disabled={extending}
							class="ml-1 rounded bg-surface-raised px-1.5 py-0.5 text-xs text-foreground-secondary transition-colors hover:bg-hover-subtle disabled:opacity-50"
						>{extending ? '...' : '+2h'}</button>
					{/if}
				</div>
			{/if}
			{#if workspace?.status === 'running'}
				<button
					onclick={() => handleAction('suspend')}
					class="rounded-lg border border-border-input px-3 py-1.5 text-sm text-foreground-secondary transition-colors hover:bg-hover-subtle"
				>Suspend</button>
			{:else if workspace?.status === 'suspended'}
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

	<MessageBanner bind:message={actionError} />

	<!-- Main content: Progress or Terminal + Agent Panel -->
	{#if isCreating}
		<div class="flex-1 overflow-hidden rounded-xl border border-border bg-surface">
			<CreationProgress workspaceId={data.workspaceId} createdAt={workspace?.created_at} onready={handleCreationReady} />
		</div>
	{:else if isInvestigation && investigation}
		<!-- Investigation layout: repo list + terminal -->
		<div class="grid min-h-0 flex-1 gap-4 lg:grid-cols-[320px_1fr]">
			<!-- Repo list panel -->
			<div class="flex min-h-0 flex-col overflow-hidden rounded-xl border border-border bg-surface">
				<div class="border-b border-border p-3">
					<input
						bind:value={repoFilter}
						placeholder="Filter repos..."
						class="w-full rounded-lg border border-border-input bg-surface px-3 py-1.5 text-sm text-foreground placeholder:text-foreground-faint focus:border-transparent focus:outline-none focus:ring-2 focus:ring-ring"
					/>
				</div>
				{#if activatingRepo}
					<div class="border-b border-border bg-info-badge-bg/30 px-3 py-2">
						<p class="text-xs text-info-text">{activationProgress}</p>
					</div>
				{/if}
				<div class="flex-1 overflow-y-auto">
					{#each filteredShallowRepos() as repoURL}
						{@const short = repoShortName(repoURL)}
						{@const activated = isActivated(repoURL)}
						<div class="flex items-center justify-between border-b border-border-subtle px-3 py-2 last:border-0">
							<div class="min-w-0 flex-1">
								<p class="truncate text-sm font-mono text-foreground" title={repoURL}>{short}</p>
							</div>
							{#if activated}
								{@const spawned = spawnedWorkspaceForRepo(repoURL)}
								<a
									href="/workspace/{spawned?.id || workspace?.id}"
									class="shrink-0 rounded-full bg-success-badge-bg px-2 py-0.5 text-xs text-success-text hover:underline"
								>Open</a>
							{:else}
								<button
									onclick={() => handleActivate(repoURL)}
									disabled={!!activatingRepo}
									class="shrink-0 rounded-lg border border-border-input px-2 py-1 text-xs text-foreground-secondary transition-colors hover:bg-hover-subtle disabled:cursor-not-allowed disabled:opacity-50"
								>
									{activatingRepo === repoURL ? 'Activating...' : 'Activate'}
								</button>
							{/if}
						</div>
					{/each}
					{#if filteredShallowRepos().length === 0}
						<p class="p-3 text-center text-sm text-foreground-faint">No repos match filter</p>
					{/if}
				</div>
			</div>

			<!-- Terminal -->
			<div class="overflow-hidden rounded-xl border border-border" style="background:#0f172a">
				<div bind:this={terminalEl} class="h-full min-h-[400px] w-full"></div>
			</div>
		</div>
	{:else}
		<div class="grid min-h-0 flex-1 gap-4 lg:grid-cols-[1fr_320px]">
			<div class="overflow-hidden rounded-xl border border-border" style="background:#0f172a">
				<div bind:this={terminalEl} class="h-full min-h-[400px] w-full"></div>
			</div>

			<div class="overflow-hidden rounded-xl border border-border bg-surface">
				<AgentPanel workspaceId={data.workspaceId} />
			</div>
		</div>
	{/if}
</div>
