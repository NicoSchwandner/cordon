<script lang="ts">
	import type { AuditEntry } from '$lib/shared/api/types';
	import { getAuditEntries } from '$lib/shared/api/client';
	import { connectAuditWS } from '$lib/shared/api/websocket';
	import TierBadge from '$lib/shared/components/TierBadge.svelte';
	import DecisionBadge from '$lib/shared/components/DecisionBadge.svelte';
	import TimeAgo from '$lib/shared/components/TimeAgo.svelte';

	let { data } = $props();

	let liveEntries: AuditEntry[] = $state([]);
	let fetchedEntries: AuditEntry[] = $state([]);
	let useServerData = $state(true);
	const entries = $derived([...liveEntries, ...(useServerData ? data.entries : fetchedEntries)]);
	let liveEnabled = $state(false);
	let cleanupWS: (() => void) | null = $state(null);
	let loading = $state(false);

	// Filters
	let tierFilter = $state('');
	let decisionFilter = $state('');
	let selectedEntry: AuditEntry | null = $state(null);

	const filtered = $derived(
		entries.filter((e) => {
			if (tierFilter && e.tier !== Number(tierFilter)) return false;
			if (decisionFilter && e.decision !== decisionFilter) return false;
			return true;
		})
	);

	function toggleLive() {
		if (liveEnabled && cleanupWS) {
			cleanupWS();
			cleanupWS = null;
			liveEnabled = false;
		} else {
			cleanupWS = connectAuditWS((entry) => {
				liveEntries = [entry, ...liveEntries];
			});
			liveEnabled = true;
		}
	}

	async function loadMore() {
		loading = true;
		try {
			const more = await getAuditEntries({ limit: 50, offset: entries.length });
			useServerData = false;
			fetchedEntries = [...fetchedEntries, ...data.entries, ...more];
		} catch {
			/* ignore */
		}
		loading = false;
	}

	async function applyFilters() {
		loading = true;
		try {
			const params: Record<string, unknown> = { limit: 50 };
			if (tierFilter) params.tier_min = Number(tierFilter);
			if (decisionFilter) params.decision = decisionFilter;
			useServerData = false;
			liveEntries = [];
			fetchedEntries = await getAuditEntries(params);
		} catch {
			/* ignore */
		}
		loading = false;
	}
</script>

<div class="space-y-4">
	<div class="flex items-center justify-between">
		<h1 class="text-2xl font-semibold tracking-tight text-foreground">Audit Log</h1>
		<button
			onclick={toggleLive}
			class="flex items-center gap-2 rounded-lg px-3 py-1.5 text-sm font-medium transition-colors {liveEnabled
				? 'bg-success-badge-bg text-success-text'
				: 'border border-border-input text-foreground-secondary hover:bg-hover-subtle'}"
		>
			<span
				class="inline-block h-2 w-2 rounded-full {liveEnabled
					? 'bg-success-text animate-pulse'
					: 'bg-foreground-faint'}"
			></span>
			{liveEnabled ? 'Live' : 'Enable Live'}
		</button>
	</div>

	<!-- Filters -->
	<div class="flex flex-wrap gap-3">
		<select
			bind:value={tierFilter}
			onchange={applyFilters}
			class="rounded-lg border border-border-input bg-surface px-3 py-1.5 text-sm text-foreground focus:border-transparent focus:outline-none focus:ring-2 focus:ring-ring"
		>
			<option value="">All Tiers</option>
			<option value="1">T1 Read</option>
			<option value="2">T2 Safe Write</option>
			<option value="3">T3 Destructive</option>
			<option value="4">T4 Forbidden</option>
		</select>

		<select
			bind:value={decisionFilter}
			onchange={applyFilters}
			class="rounded-lg border border-border-input bg-surface px-3 py-1.5 text-sm text-foreground focus:border-transparent focus:outline-none focus:ring-2 focus:ring-ring"
		>
			<option value="">All Decisions</option>
			<option value="allowed">Allowed</option>
			<option value="denied">Denied</option>
			<option value="blocked">Blocked</option>
		</select>
	</div>

	<!-- Table -->
	<div class="overflow-hidden rounded-xl border border-border bg-surface">
		<div class="overflow-x-auto">
			<table class="min-w-full text-sm">
				<thead>
					<tr class="border-b border-border">
						<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-foreground-muted">Time</th>
						<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-foreground-muted">Caller</th>
						<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-foreground-muted">Operation</th>
						<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-foreground-muted">Target</th>
						<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-foreground-muted">Tier</th>
						<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-foreground-muted">Decision</th>
						<th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-foreground-muted whitespace-nowrap">Duration</th>
					</tr>
				</thead>
				<tbody>
					{#each filtered as entry (entry.id)}
						<tr
							class="cursor-pointer border-b border-border-subtle last:border-0 transition-colors hover:bg-hover-subtle {entry === filtered[0] && liveEnabled ? 'animate-fade-in' : ''}"
							onclick={() => (selectedEntry = selectedEntry?.id === entry.id ? null : entry)}
						>
							<td class="px-4 py-3"><TimeAgo timestamp={entry.timestamp} /></td>
							<td class="px-4 py-3 text-foreground-secondary">{entry.caller}</td>
							<td class="px-4 py-3 font-mono text-xs text-foreground-secondary">{entry.operation}</td>
							<td class="max-w-48 truncate px-4 py-3 text-foreground-muted">{entry.target || '-'}</td>
							<td class="px-4 py-3"><TierBadge tier={entry.tier} /></td>
							<td class="px-4 py-3"><DecisionBadge decision={entry.decision} /></td>
							<td class="px-4 py-3 text-foreground-muted whitespace-nowrap">{entry.duration_ms}ms</td>
						</tr>
						{#if selectedEntry?.id === entry.id}
							<tr>
								<td colspan="7" class="bg-surface-inset px-5 py-4">
									<div class="space-y-1 text-xs">
										<div><span class="text-foreground-muted">ID:</span> <span class="font-mono text-foreground-secondary">{entry.id}</span></div>
										<div><span class="text-foreground-muted">Timestamp:</span> <span class="text-foreground-secondary">{new Date(entry.timestamp).toLocaleString()}</span></div>
										<div><span class="text-foreground-muted">Tier:</span> <span class="text-foreground-secondary">{entry.tier_name}</span></div>
										<div class="pt-1"><span class="text-foreground-muted">Detail:</span></div>
										<pre class="mt-1 overflow-x-auto rounded-lg border border-border-subtle bg-surface p-3 font-mono text-xs text-foreground-secondary">{entry.detail || 'N/A'}</pre>
									</div>
								</td>
							</tr>
						{/if}
					{/each}
				</tbody>
			</table>
		</div>
	</div>

	{#if filtered.length >= 50}
		<div class="text-center">
			<button
				onclick={loadMore}
				disabled={loading}
				class="rounded-lg border border-border-input px-4 py-2 text-sm text-foreground-secondary transition-colors hover:bg-hover-subtle disabled:opacity-50"
			>
				{loading ? 'Loading...' : 'Load More'}
			</button>
		</div>
	{/if}
</div>
