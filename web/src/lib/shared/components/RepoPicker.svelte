<script lang="ts">
	import { listOrgRepos, listRepoBranches, getDefaultBranch } from '$lib/shared/api/client';
	import type { OrgRepo, GitBranch, RepoConfig } from '$lib/shared/api/types';

	type RepoRow = {
		mode: 'org' | 'url';
		// org mode
		selectedRepo: OrgRepo | null;
		repoSearch: string;
		// url mode
		url: string;
		// shared
		branch: string;
		detectedBranch: string;
		branches: GitBranch[];
		branchSearch: string;
		showRepoDropdown: boolean;
		showBranchDropdown: boolean;
		serviceContainer: boolean;
	};

	interface Props {
		org: string;
		orgRepos: OrgRepo[];
		rows: RepoRow[];
		onchange?: () => void;
	}

	let { org, orgRepos, rows = $bindable(), onchange }: Props = $props();

	let branchTimers: Map<number, ReturnType<typeof setTimeout>> = new Map();

	function emptyRow(): RepoRow {
		return {
			mode: org ? 'org' : 'url',
			selectedRepo: null,
			repoSearch: '',
			url: '',
			branch: '',
			detectedBranch: '',
			branches: [],
			branchSearch: '',
			showRepoDropdown: false,
			showBranchDropdown: false,
			serviceContainer: false,
		};
	}

	export function addRow() {
		rows.push(emptyRow());
	}

	export function createEmptyRow(): RepoRow {
		return emptyRow();
	}

	function removeRow(index: number) {
		rows.splice(index, 1);
		branchTimers.delete(index);
		onchange?.();
	}

	function filteredRepos(search: string): OrgRepo[] {
		const q = search.toLowerCase().trim();
		if (!q) return orgRepos.filter(r => !r.archived);
		return orgRepos.filter(r =>
			!r.archived && (
				r.name.toLowerCase().includes(q) ||
				(r.description || '').toLowerCase().includes(q)
			)
		);
	}

	function selectRepo(index: number, repo: OrgRepo) {
		rows[index].selectedRepo = repo;
		rows[index].repoSearch = repo.name;
		rows[index].showRepoDropdown = false;
		rows[index].detectedBranch = repo.default_branch;
		rows[index].branch = '';
		loadBranches(index, repo.full_name);
		onchange?.();
	}

	async function loadBranches(index: number, fullName: string) {
		const [owner, repo] = fullName.split('/');
		try {
			rows[index].branches = await listRepoBranches(owner, repo);
		} catch {
			rows[index].branches = [];
		}
	}

	function selectBranch(index: number, branch: string) {
		rows[index].branch = branch;
		rows[index].branchSearch = branch;
		rows[index].showBranchDropdown = false;
		onchange?.();
	}

	function filteredBranches(row: RepoRow): GitBranch[] {
		const q = row.branchSearch.toLowerCase().trim();
		if (!q) return row.branches;
		return row.branches.filter(b => b.name.toLowerCase().includes(q));
	}

	function toggleMode(index: number) {
		const newMode = rows[index].mode === 'org' ? 'url' : 'org';
		rows[index] = { ...emptyRow(), mode: newMode };
		onchange?.();
	}

	function detectBranchFromUrl(index: number) {
		const timer = branchTimers.get(index);
		if (timer) clearTimeout(timer);

		const url = rows[index].url.trim();
		if (!url) {
			rows[index].detectedBranch = '';
			return;
		}

		branchTimers.set(
			index,
			setTimeout(async () => {
				try {
					const result = await getDefaultBranch(url);
					rows[index].detectedBranch = result.default_branch;
				} catch {
					rows[index].detectedBranch = '';
				}
			}, 500)
		);
	}

	export function getRepoConfigs(): RepoConfig[] {
		return rows
			.filter(r => r.mode === 'org' ? r.selectedRepo != null : r.url.trim().length > 0)
			.map((r, i) => {
				const base = {
					branch: r.branch || undefined,
					primary: i === 0,
					service_container: i > 0 && r.serviceContainer ? true : undefined,
				};
				if (r.mode === 'org' && r.selectedRepo) {
					return { url: `github.com/${r.selectedRepo.full_name}`, ...base };
				}
				return { url: r.url.trim(), ...base };
			});
	}

	export function getPrimaryName(): string {
		const first = rows.find(r =>
			r.mode === 'org' ? r.selectedRepo != null : r.url.trim().length > 0
		);
		if (!first) return '';
		if (first.mode === 'org' && first.selectedRepo) return first.selectedRepo.name;
		const parts = first.url.replace(/\.git$/, '').split('/');
		return parts[parts.length - 1] || '';
	}
</script>

<div class="space-y-2">
	{#each rows as row, i}
		<div class="flex items-start gap-2">
			<div class="grid flex-1 gap-2 sm:grid-cols-2">
				<!-- Repo selection -->
				<div class="relative">
					{#if row.mode === 'org' && org}
						<div class="relative">
							<input
								type="text"
								bind:value={row.repoSearch}
								onfocus={() => { row.showRepoDropdown = true; }}
								onblur={() => { setTimeout(() => { row.showRepoDropdown = false; }, 200); }}
								placeholder="Search repos..."
								class="w-full rounded-lg border border-border-input bg-surface px-3 py-2 text-sm text-foreground placeholder:text-foreground-faint focus:border-transparent focus:outline-none focus:ring-2 focus:ring-ring"
							/>
							{#if row.showRepoDropdown}
								<div class="absolute z-20 mt-1 max-h-48 w-full overflow-y-auto rounded-lg border border-border bg-surface shadow-lg">
									{#each filteredRepos(row.repoSearch) as repo}
										<button
											type="button"
											onmousedown={() => selectRepo(i, repo)}
											class="flex w-full items-start gap-2 px-3 py-2 text-left text-sm hover:bg-hover-subtle {row.selectedRepo?.name === repo.name ? 'bg-hover-subtle' : ''}"
										>
											<div class="min-w-0 flex-1">
												<div class="truncate font-medium text-foreground">{repo.name}</div>
												{#if repo.description}
													<div class="truncate text-xs text-foreground-faint">{repo.description}</div>
												{/if}
											</div>
											{#if repo.language}
												<span class="shrink-0 text-xs text-foreground-faint">{repo.language}</span>
											{/if}
										</button>
									{:else}
										<div class="px-3 py-2 text-sm text-foreground-faint">No repos found</div>
									{/each}
								</div>
							{/if}
						</div>
					{:else}
						<input
							type="text"
							bind:value={row.url}
							oninput={() => detectBranchFromUrl(i)}
							placeholder="e.g. github.com/org/repo"
							class="w-full rounded-lg border border-border-input bg-surface px-3 py-2 text-sm text-foreground placeholder:text-foreground-faint focus:border-transparent focus:outline-none focus:ring-2 focus:ring-ring"
						/>
					{/if}
					<div class="mt-0.5 flex items-center gap-2">
						{#if i === 0 && rows.filter(r => r.mode === 'org' ? r.selectedRepo : r.url.trim()).length > 1}
							<span class="text-xs text-foreground-faint">primary</span>
						{/if}
						{#if i > 0}
							<label class="flex items-center gap-1 text-xs text-foreground-faint cursor-pointer">
								<input type="checkbox" bind:checked={row.serviceContainer} class="h-3 w-3 rounded accent-primary" />
								Own container
							</label>
						{/if}
						{#if org}
							<button
								type="button"
								onclick={() => toggleMode(i)}
								class="text-xs text-foreground-faint transition-colors hover:text-foreground-muted"
							>
								{row.mode === 'org' ? 'Use URL instead' : 'Pick from org'}
							</button>
						{/if}
					</div>
				</div>

				<!-- Branch selection -->
				<div class="relative">
					{#if row.branches.length > 0}
						<input
							type="text"
							bind:value={row.branchSearch}
							onfocus={() => { row.showBranchDropdown = true; }}
							onblur={() => { setTimeout(() => { row.showBranchDropdown = false; }, 200); }}
							placeholder={row.detectedBranch || 'branch (auto-detect)'}
							class="w-full rounded-lg border border-border-input bg-surface px-3 py-2 text-sm text-foreground placeholder:text-foreground-faint focus:border-transparent focus:outline-none focus:ring-2 focus:ring-ring"
						/>
						{#if row.showBranchDropdown}
							<div class="absolute z-20 mt-1 max-h-48 w-full overflow-y-auto rounded-lg border border-border bg-surface shadow-lg">
								{#each filteredBranches(row) as branch}
									<button
										type="button"
										onmousedown={() => selectBranch(i, branch.name)}
										class="w-full px-3 py-1.5 text-left text-sm hover:bg-hover-subtle {row.branch === branch.name ? 'bg-hover-subtle font-medium' : ''}"
									>
										{branch.name}
										{#if branch.name === row.detectedBranch}
											<span class="text-xs text-foreground-faint">(default)</span>
										{/if}
									</button>
								{:else}
									<div class="px-3 py-2 text-sm text-foreground-faint">No branches found</div>
								{/each}
							</div>
						{/if}
					{:else}
						<input
							type="text"
							bind:value={row.branch}
							placeholder={row.detectedBranch || 'branch (auto-detect)'}
							class="w-full rounded-lg border border-border-input bg-surface px-3 py-2 text-sm text-foreground placeholder:text-foreground-faint focus:border-transparent focus:outline-none focus:ring-2 focus:ring-ring"
						/>
					{/if}
				</div>
			</div>

			<!-- Remove button -->
			{#if i > 0}
				<button
					type="button"
					onclick={() => removeRow(i)}
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
	onclick={() => addRow()}
	class="mt-2 text-xs text-foreground-muted transition-colors hover:text-foreground"
>
	+ Add another repo
</button>
