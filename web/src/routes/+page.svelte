<script lang="ts">
	import TierBadge from '$lib/shared/components/TierBadge.svelte';
	import DecisionBadge from '$lib/shared/components/DecisionBadge.svelte';
	import TimeAgo from '$lib/shared/components/TimeAgo.svelte';
	import StatusDot from '$lib/shared/components/StatusDot.svelte';
	import RepoPicker from '$lib/shared/components/RepoPicker.svelte';
	import { getContext } from 'svelte';
	import { createWorkspace, listOrgRepos } from '$lib/shared/api/client';
	import type { WorkspaceResponse, OrgRepo } from '$lib/shared/api/types';

	const getHealthy = getContext<() => boolean | null>('healthy');
	const healthy = $derived(getHealthy());

	let { data } = $props();

	let showForm = $state(false);
	let newName = $state('');
	let creating = $state(false);
	let extraWorkspaces: WorkspaceResponse[] = $state([]);
	const workspaces = $derived([...extraWorkspaces, ...data.workspaces]);

	// Org catalog state
	let org = $state('');
	let orgInput = $state('');
	let orgRepos: OrgRepo[] = $state([]);
	let orgLoading = $state(false);
	let orgError = $state('');

	// RepoPicker ref and rows state
	let picker: RepoPicker | undefined = $state();
	let repoRows = $state([makeEmptyRow()]);

	function makeEmptyRow() {
		return {
			mode: 'org' as const,
			selectedRepo: null,
			repoSearch: '',
			url: '',
			branch: '',
			detectedBranch: '',
			branches: [] as import('$lib/shared/api/types').GitBranch[],
			branchSearch: '',
			showRepoDropdown: false,
			showBranchDropdown: false,
		};
	}

	async function loadOrg() {
		const target = orgInput.trim();
		if (!target) {
			org = '';
			orgRepos = [];
			return;
		}
		orgLoading = true;
		orgError = '';
		try {
			orgRepos = await listOrgRepos(target);
			org = target;
		} catch (e) {
			orgError = e instanceof Error ? e.message : 'Failed to load org';
			orgRepos = [];
			org = '';
		}
		orgLoading = false;
	}

	function repoShortName(repo: string): string {
		const parts = repo.replace(/\.git$/, '').split('/');
		return parts[parts.length - 1] || '';
	}

	async function handleCreate() {
		if (!picker) return;
		const repoConfigs = picker.getRepoConfigs();
		const primaryName = picker.getPrimaryName();
		const name = newName.trim() || primaryName || 'workspace';

		creating = true;
		try {
			const req: import('$lib/shared/api/types').CreateWorkspaceRequest = { name };
			if (repoConfigs.length > 0) {
				req.repos = repoConfigs;
			}
			const ws = await createWorkspace(req);
			window.location.href = `/workspace/${ws.id}`;
		} catch (e) {
			alert(e instanceof Error ? e.message : 'Failed to create workspace');
		}
		creating = false;
	}
</script>

<div class="space-y-8">
	<div class="flex items-center justify-between">
		<h1 class="text-2xl font-semibold tracking-tight text-foreground">Dashboard</h1>
		<div class="flex items-center gap-2">
			<span
				class="inline-block h-2.5 w-2.5 rounded-full {healthy
					? 'bg-success-text'
					: healthy === false
						? 'bg-danger-solid'
						: 'bg-foreground-faint'}"
			></span>
			<span class="text-sm text-foreground-muted"
				>{healthy ? 'System healthy' : healthy === false ? 'Unhealthy' : 'Checking...'}</span
			>
		</div>
	</div>

	<!-- Workspaces -->
	<section>
		<div class="mb-3 flex items-center justify-between">
			<h2 class="text-xs font-medium uppercase tracking-wider text-foreground-muted">
				Workspaces
			</h2>
			<button
				onclick={() => (showForm = !showForm)}
				class="rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-on-primary transition-colors hover:bg-primary-hover"
			>
				{showForm ? 'Cancel' : 'New Workspace'}
			</button>
		</div>

		{#if showForm}
			<form
				onsubmit={(e) => {
					e.preventDefault();
					handleCreate();
				}}
				class="mb-4 space-y-3 rounded-xl border border-border bg-surface p-4"
			>
				<!-- Org selector -->
				<div>
					<label for="org-input" class="mb-1 block text-xs font-medium text-foreground-muted">
						GitHub Organization <span class="font-normal text-foreground-faint">(optional — enables repo picker)</span>
					</label>
					<div class="flex gap-2">
						<input
							id="org-input"
							bind:value={orgInput}
							placeholder="e.g. WintDev"
							onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); loadOrg(); } }}
							class="flex-1 rounded-lg border border-border-input bg-surface px-3 py-2 text-sm text-foreground placeholder:text-foreground-faint focus:border-transparent focus:outline-none focus:ring-2 focus:ring-ring"
						/>
						<button
							type="button"
							onclick={loadOrg}
							disabled={orgLoading || !orgInput.trim()}
							class="rounded-lg border border-border-input px-3 py-2 text-sm text-foreground-secondary transition-colors hover:bg-hover-subtle disabled:cursor-not-allowed disabled:opacity-50"
						>
							{orgLoading ? 'Loading...' : org ? 'Reload' : 'Load'}
						</button>
					</div>
					{#if orgError}
						<p class="mt-1 text-xs text-danger-text">{orgError}</p>
					{/if}
					{#if org && orgRepos.length > 0}
						<p class="mt-1 text-xs text-foreground-faint">
							{orgRepos.filter(r => !r.archived).length} repos available
							{#if orgRepos.some(r => r.archived)}
								({orgRepos.filter(r => r.archived).length} archived hidden)
							{/if}
						</p>
					{/if}
				</div>

				<!-- Repo rows -->
				<div>
					<label class="mb-1 block text-xs font-medium text-foreground-muted">
						Repositories <span class="font-normal text-foreground-faint">(optional — leave empty for bare container)</span>
					</label>
					<RepoPicker
						bind:this={picker}
						{org}
						{orgRepos}
						bind:rows={repoRows}
					/>
				</div>

				<div>
					<label for="ws-name" class="mb-1 block text-xs font-medium text-foreground-muted">Name <span class="font-normal text-foreground-faint">(auto-fills from primary repo if empty)</span></label>
					<input
						id="ws-name"
						bind:value={newName}
						placeholder={picker?.getPrimaryName() || 'workspace'}
						class="w-full rounded-lg border border-border-input bg-surface px-3 py-2 text-sm text-foreground placeholder:text-foreground-faint focus:border-transparent focus:outline-none focus:ring-2 focus:ring-ring"
					/>
				</div>
				{#if picker && picker.getRepoConfigs().length > 0}
					<p class="text-xs text-foreground-faint">Building from devcontainer may take a few minutes on first run.</p>
				{/if}
				<div class="flex gap-2">
					<button
						type="submit"
						disabled={creating}
						class="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-on-primary transition-colors hover:bg-primary-hover disabled:cursor-not-allowed disabled:opacity-50"
					>
						{creating ? 'Creating...' : 'Create Workspace'}
					</button>
					<button
						type="button"
						onclick={() => (showForm = false)}
						class="rounded-lg border border-border-input px-4 py-2 text-sm text-foreground-secondary transition-colors hover:bg-hover-subtle"
					>
						Cancel
					</button>
				</div>
			</form>
		{/if}

		{#if workspaces.length === 0}
			<div
				class="rounded-xl border border-border bg-surface p-6 text-center text-sm text-foreground-muted"
			>
				No workspaces yet. Create one to get started.
			</div>
		{:else}
			<div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
				{#each workspaces as ws}
					{@const primary = ws.repos?.find(r => r.primary) || ws.repos?.[0]}
					{@const extraCount = (ws.repos?.length || 0) - 1}
					<a
						href="/workspace/{ws.id}"
						class="rounded-xl border border-border bg-surface p-5 transition-colors hover:bg-hover-subtle"
					>
						<div class="mb-2 flex items-center justify-between">
							<span class="font-medium text-foreground">{ws.name}</span>
							<StatusDot status={ws.status} />
						</div>
						{#if primary}
							<div class="mb-1 truncate text-xs font-mono text-foreground-secondary">{primary.url}</div>
							<div class="flex items-center gap-2">
								<span class="text-xs text-foreground-faint">{primary.branch || 'main'}</span>
								{#if extraCount > 0}
									<span class="rounded-full bg-surface-raised px-1.5 py-0.5 text-xs text-foreground-muted">+{extraCount} repo{extraCount > 1 ? 's' : ''}</span>
								{/if}
							</div>
						{:else}
							<div class="text-xs text-foreground-faint">
								Created {new Date(ws.created_at).toLocaleDateString()}
							</div>
						{/if}
					</a>
				{/each}
			</div>
		{/if}
	</section>

	<!-- Recent Activity -->
	<section>
		<h2 class="mb-3 text-xs font-medium uppercase tracking-wider text-foreground-muted">
			Recent Activity
		</h2>

		{#if data.recentAudit.length === 0}
			<div
				class="rounded-xl border border-border bg-surface p-6 text-center text-sm text-foreground-muted"
			>
				No audit entries yet.
			</div>
		{:else}
			<div class="overflow-hidden rounded-xl border border-border bg-surface">
				<div class="overflow-x-auto">
					<table class="min-w-full text-sm">
						<thead>
							<tr class="border-b border-border">
								<th
									class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-foreground-muted"
									>Time</th
								>
								<th
									class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-foreground-muted"
									>Operation</th
								>
								<th
									class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-foreground-muted"
									>Caller</th
								>
								<th
									class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-foreground-muted"
									>Tier</th
								>
								<th
									class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-foreground-muted"
									>Decision</th
								>
							</tr>
						</thead>
						<tbody>
							{#each data.recentAudit as entry}
								<tr class="border-b border-border-subtle last:border-0">
									<td class="px-4 py-3"><TimeAgo timestamp={entry.timestamp} /></td>
									<td class="px-4 py-3 font-mono text-xs text-foreground-secondary">
										{entry.operation}
									</td>
									<td class="px-4 py-3 text-foreground-secondary">{entry.caller}</td>
									<td class="px-4 py-3"><TierBadge tier={entry.tier} /></td>
									<td class="px-4 py-3"><DecisionBadge decision={entry.decision} /></td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			</div>
		{/if}
	</section>
</div>
