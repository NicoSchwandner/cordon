<script lang="ts">
	import TierBadge from '$lib/shared/components/TierBadge.svelte';
	import DecisionBadge from '$lib/shared/components/DecisionBadge.svelte';
	import TimeAgo from '$lib/shared/components/TimeAgo.svelte';
	import StatusDot from '$lib/shared/components/StatusDot.svelte';
	import { createWorkspace } from '$lib/shared/api/client';
	import type { WorkspaceResponse } from '$lib/shared/api/types';

	let { data } = $props();

	let showForm = $state(false);
	let newName = $state('');
	let newRepo = $state('');
	let newBranch = $state('');
	let creating = $state(false);
	let extraWorkspaces: WorkspaceResponse[] = $state([]);
	const workspaces = $derived([...extraWorkspaces, ...data.workspaces]);
	const hasRepo = $derived(newRepo.trim().length > 0);

	async function handleCreate() {
		const name = newName.trim() || repoShortName(newRepo) || 'workspace';
		creating = true;
		try {
			const req: import('$lib/shared/api/types').CreateWorkspaceRequest = { name };
			if (newRepo.trim()) {
				req.repo = newRepo.trim();
				req.branch = newBranch.trim() || 'development';
			}
			const ws = await createWorkspace(req);
			extraWorkspaces = [ws, ...extraWorkspaces];
			newName = '';
			newRepo = '';
			newBranch = '';
			showForm = false;
		} catch (e) {
			alert(e instanceof Error ? e.message : 'Failed to create workspace');
		}
		creating = false;
	}

	function repoShortName(repo: string): string {
		const parts = repo.replace(/\.git$/, '').split('/');
		return parts[parts.length - 1] || '';
	}
</script>

<div class="space-y-8">
	<div class="flex items-center justify-between">
		<h1 class="text-2xl font-semibold tracking-tight text-foreground">Dashboard</h1>
		<div class="flex items-center gap-2">
			<span
				class="inline-block h-2.5 w-2.5 rounded-full {data.healthy
					? 'bg-success-text'
					: 'bg-danger-solid'}"
			></span>
			<span class="text-sm text-foreground-muted"
				>{data.healthy ? 'System healthy' : 'Unhealthy'}</span
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
				<div>
					<label for="ws-repo" class="mb-1 block text-xs font-medium text-foreground-muted">Repository URL <span class="font-normal text-foreground-faint">(optional — leave empty for bare container)</span></label>
					<input
						id="ws-repo"
						bind:value={newRepo}
						placeholder="e.g. github.com/WintDev/Core"
						class="w-full rounded-lg border border-border-input bg-surface px-3 py-2 text-sm text-foreground placeholder:text-foreground-faint focus:border-transparent focus:outline-none focus:ring-2 focus:ring-ring"
					/>
				</div>
				{#if hasRepo}
					<div>
						<label for="ws-branch" class="mb-1 block text-xs font-medium text-foreground-muted">Branch</label>
						<input
							id="ws-branch"
							bind:value={newBranch}
							placeholder="development"
							class="w-full rounded-lg border border-border-input bg-surface px-3 py-2 text-sm text-foreground placeholder:text-foreground-faint focus:border-transparent focus:outline-none focus:ring-2 focus:ring-ring"
						/>
					</div>
				{/if}
				<div>
					<label for="ws-name" class="mb-1 block text-xs font-medium text-foreground-muted">Name <span class="font-normal text-foreground-faint">(auto-fills from repo if empty)</span></label>
					<input
						id="ws-name"
						bind:value={newName}
						placeholder={hasRepo ? repoShortName(newRepo) || 'workspace' : 'workspace'}
						class="w-full rounded-lg border border-border-input bg-surface px-3 py-2 text-sm text-foreground placeholder:text-foreground-faint focus:border-transparent focus:outline-none focus:ring-2 focus:ring-ring"
					/>
				</div>
				{#if hasRepo}
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
					<a
						href="/workspace/{ws.id}"
						class="rounded-xl border border-border bg-surface p-5 transition-colors hover:bg-hover-subtle"
					>
						<div class="mb-2 flex items-center justify-between">
							<span class="font-medium text-foreground">{ws.name}</span>
							<StatusDot status={ws.status} />
						</div>
						{#if ws.repo}
							<div class="mb-1 truncate text-xs font-mono text-foreground-secondary">{ws.repo}</div>
							<div class="text-xs text-foreground-faint">{ws.branch || 'main'}</div>
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
