<script lang="ts">
	let activeTab: 'egress' | 'overrides' | 'secrets' = $state('egress');

	const egressAllowlist = [
		'github.com',
		'api.anthropic.com',
		'registry.npmjs.org',
		'nuget.org',
		'*.example.com'
	];

	const tierOverrides = [
		{ pattern: 'DELETE FROM audit_log', tier: 4 },
		{ pattern: 'DROP TABLE temp_*', tier: 2 }
	];

	const secrets = [
		{ name: 'DATABASE_URL', placeholder: 'cordon-placeholder-database-url' },
		{ name: 'API_KEY', placeholder: 'cordon-placeholder-api-key' },
		{ name: 'NUGET_FEED_TOKEN', placeholder: 'cordon-placeholder-nuget-token' }
	];
</script>

<div class="space-y-6">
	<h1 class="text-2xl font-semibold tracking-tight text-foreground">Settings</h1>

	<!-- Tabs -->
	<div class="flex gap-1 rounded-xl border border-border bg-surface p-1">
		{#each [
			{ id: 'egress', label: 'Egress Allowlist' },
			{ id: 'overrides', label: 'Tier Overrides' },
			{ id: 'secrets', label: 'Secrets' }
		] as tab}
			<button
				onclick={() => (activeTab = tab.id as typeof activeTab)}
				class="rounded-lg px-4 py-2 text-sm font-medium transition-colors {activeTab === tab.id
					? 'bg-sidebar-active text-foreground'
					: 'text-foreground-muted hover:text-foreground hover:bg-hover-subtle'}"
			>
				{tab.label}
			</button>
		{/each}
	</div>

	<!-- Egress -->
	{#if activeTab === 'egress'}
		<div class="rounded-xl border border-border bg-surface p-5">
			<p class="mb-4 text-sm text-foreground-tertiary">
				Hosts that workspace processes are allowed to connect to. All other egress is blocked.
			</p>
			<ul class="space-y-1">
				{#each egressAllowlist as host}
					<li class="flex items-center gap-2 rounded-lg bg-surface-inset px-3 py-2.5">
						<span class="h-2 w-2 rounded-full bg-success-text"></span>
						<span class="font-mono text-sm text-foreground">{host}</span>
					</li>
				{/each}
			</ul>
			<p class="mt-4 text-xs text-foreground-faint">
				Managed via .cordon.yaml. API-based management coming soon.
			</p>
		</div>
	{/if}

	<!-- Tier Overrides -->
	{#if activeTab === 'overrides'}
		<div class="rounded-xl border border-border bg-surface p-5">
			<p class="mb-4 text-sm text-foreground-tertiary">
				Pattern-based overrides that escalate or de-escalate the default tier for specific
				operations.
			</p>
			<div class="overflow-x-auto">
				<table class="min-w-full text-sm">
					<thead>
						<tr class="border-b border-border">
							<th
								class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-foreground-muted"
								>Pattern</th
							>
							<th
								class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-foreground-muted"
								>Forced Tier</th
							>
						</tr>
					</thead>
					<tbody>
						{#each tierOverrides as override}
							<tr class="border-b border-border-subtle last:border-0">
								<td class="px-4 py-3 font-mono text-foreground">{override.pattern}</td>
								<td class="px-4 py-3">
									<span
										class="inline-block rounded-full bg-surface-raised px-2.5 py-1 text-xs font-medium text-foreground-secondary"
									>
										Tier {override.tier}
									</span>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
			<p class="mt-4 text-xs text-foreground-faint">
				Managed via .cordon.yaml. API-based management coming soon.
			</p>
		</div>
	{/if}

	<!-- Secrets -->
	{#if activeTab === 'secrets'}
		<div class="rounded-xl border border-border bg-surface p-5">
			<p class="mb-4 text-sm text-foreground-tertiary">
				Secret names and their placeholder tokens. Real values are never exposed to the workspace
				or UI.
			</p>
			<div class="space-y-2">
				{#each secrets as secret}
					<div
						class="flex items-center justify-between rounded-lg bg-surface-inset px-4 py-3"
					>
						<div>
							<span class="font-mono text-sm font-medium text-foreground">{secret.name}</span>
							<span class="ml-2 text-xs text-foreground-faint">&#8594; {secret.placeholder}</span>
						</div>
						<span
							class="rounded-full bg-success-badge-bg px-2.5 py-1 text-xs font-medium text-success-text"
							>configured</span
						>
					</div>
				{/each}
			</div>
			<p class="mt-4 text-xs text-foreground-faint">
				Real credentials are injected at the proxy level and never leave the server.
			</p>
		</div>
	{/if}
</div>
