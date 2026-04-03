<script lang="ts">
	import TierBadge from '$lib/shared/components/TierBadge.svelte';
	import DecisionBadge from '$lib/shared/components/DecisionBadge.svelte';
	import TimeAgo from '$lib/shared/components/TimeAgo.svelte';
	import StatusDot from '$lib/shared/components/StatusDot.svelte';
	import { getContext } from 'svelte';
	import { createWorkspace, getDefaultBranch } from '$lib/shared/api/client';
	import type { WorkspaceResponse, RepoConfig } from '$lib/shared/api/types';

	const getHealthy = getContext<() => boolean | null>('healthy');
	const healthy = $derived(getHealthy());

	let { data } = $props();

	let showForm = $state(false);
	let newName = $state('');
	let creating = $state(false);
	let extraWorkspaces: WorkspaceResponse[] = $state([]);
	const workspaces = $derived([...extraWorkspaces, ...data.workspaces]);

	// Multi-repo form state
	interface RepoRow {
		url: string;
		branch: string;
		detectedBranch: string;
	}

	let repos = $state<RepoRow[]>([{ url: '', branch: '', detectedBranch: '' }]);
	let branchTimers: Map<number, ReturnType<typeof setTimeout>> = new Map();

	function addRepo() {
		repos.push({ url: '', branch: '', detectedBranch: '' });
	}

	function removeRepo(index: number) {
		repos.splice(index, 1);
		branchTimers.delete(index);
	}

	function detectBranch(index: number) {
		const timer = branchTimers.get(index);
		if (timer) clearTimeout(timer);

		const url = repos[index].url.trim();
		if (!url) {
			repos[index].detectedBranch = '';
			return;
		}

		branchTimers.set(
			index,
			setTimeout(async () => {
				try {
					const result = await getDefaultBranch(url);
					repos[index].detectedBranch = result.default_branch;
				} catch {
					repos[index].detectedBranch = '';
				}
			}, 500)
		);
	}

	const hasRepos = $derived(repos.some((r) => r.url.trim().length > 0));
	const primaryRepo = $derived(repos.find((r) => r.url.trim().length > 0));

	async function handleCreate() {
		const repoConfigs: RepoConfig[] = repos
			.filter((r) => r.url.trim().length > 0)
			.map((r, i) => ({
				url: r.url.trim(),
				branch: r.branch.trim() || undefined,
				primary: i === 0
			}));

		const name = newName.trim() || repoShortName(primaryRepo?.url || '') || 'workspace';
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
				<!-- Repo rows -->
				<div>
					<div class="mb-1 flex items-center justify-between">
						<label class="block text-xs font-medium text-foreground-muted">Repositories <span class="font-normal text-foreground-faint">(optional — leave empty for bare container)</span></label>
					</div>
					<div class="space-y-2">
						{#each repos as repo, i}
							<div class="flex items-start gap-2">
								<div class="grid flex-1 gap-2 sm:grid-cols-2">
									<div>
										<input
											bind:value={repo.url}
											oninput={() => detectBranch(i)}
											placeholder="e.g. github.com/WintDev/Core"
											class="w-full rounded-lg border border-border-input bg-surface px-3 py-2 text-sm text-foreground placeholder:text-foreground-faint focus:border-transparent focus:outline-none focus:ring-2 focus:ring-ring"
										/>
										{#if i === 0 && repos.filter(r => r.url.trim()).length > 1}
											<span class="mt-0.5 block text-xs text-foreground-faint">primary (devcontainer source)</span>
										{/if}
									</div>
									<input
										bind:value={repo.branch}
										placeholder={repo.detectedBranch || 'branch (auto-detect)'}
										class="w-full rounded-lg border border-border-input bg-surface px-3 py-2 text-sm text-foreground placeholder:text-foreground-faint focus:border-transparent focus:outline-none focus:ring-2 focus:ring-ring"
									/>
								</div>
								{#if i > 0}
									<button
										type="button"
										onclick={() => removeRepo(i)}
										class="mt-2 text-foreground-faint transition-colors hover:text-danger-text"
										title="Remove repo"
									>
										&times;
									</button>
								{:else}
									<div class="w-4"></div>
								{/if}
							</div>
						{/each}
					</div>
					<button
						type="button"
						onclick={addRepo}
						class="mt-2 text-xs text-foreground-muted transition-colors hover:text-foreground"
					>
						+ Add another repo
					</button>
				</div>

				<div>
					<label for="ws-name" class="mb-1 block text-xs font-medium text-foreground-muted">Name <span class="font-normal text-foreground-faint">(auto-fills from primary repo if empty)</span></label>
					<input
						id="ws-name"
						bind:value={newName}
						placeholder={hasRepos ? repoShortName(primaryRepo?.url || '') || 'workspace' : 'workspace'}
						class="w-full rounded-lg border border-border-input bg-surface px-3 py-2 text-sm text-foreground placeholder:text-foreground-faint focus:border-transparent focus:outline-none focus:ring-2 focus:ring-ring"
					/>
				</div>
				{#if hasRepos}
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
