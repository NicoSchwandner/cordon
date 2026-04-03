<script lang="ts">
	import type { AuditEntry } from '$lib/shared/api/types';
	import { connectAuditWS } from '$lib/shared/api/websocket';
	import TierBadge from '$lib/shared/components/TierBadge.svelte';
	import DecisionBadge from '$lib/shared/components/DecisionBadge.svelte';

	let { workspaceId = '' }: { workspaceId?: string } = $props();

	let entries: AuditEntry[] = $state([]);
	let paused = $state(false);
	let buffer: AuditEntry[] = $state([]);

	$effect(() => {
		const cleanup = connectAuditWS((entry) => {
			if (workspaceId && entry.workspace_id !== workspaceId) return;
			if (paused) {
				buffer.push(entry);
			} else {
				entries = [entry, ...entries].slice(0, 200);
			}
		});
		return cleanup;
	});

	function togglePause() {
		if (paused) {
			entries = [...buffer.reverse(), ...entries].slice(0, 200);
			buffer = [];
		}
		paused = !paused;
	}
</script>

<div class="flex h-full flex-col">
	<div class="flex items-center justify-between border-b border-border px-3 py-2.5">
		<h3 class="text-xs font-medium uppercase tracking-wider text-foreground-muted">Agent Operations</h3>
		<button
			onclick={togglePause}
			class="rounded-md px-2 py-1 text-xs transition-colors {paused
				? 'bg-warning-badge-bg text-warning-text'
				: 'text-foreground-muted hover:bg-hover-subtle hover:text-foreground'}"
		>
			{paused ? `Resume (${buffer.length})` : 'Pause'}
		</button>
	</div>

	<div class="flex-1 overflow-auto">
		{#if entries.length === 0}
			<p class="p-4 text-center text-sm text-foreground-faint">No operations yet</p>
		{:else}
			{#each entries as entry (entry.id)}
				<div class="animate-fade-in border-b border-border-subtle px-3 py-2">
					<div class="flex items-center gap-2">
						<TierBadge tier={entry.tier} />
						<DecisionBadge decision={entry.decision} />
						<span class="text-xs text-foreground-faint">{entry.caller}</span>
					</div>
					<p class="mt-1 truncate font-mono text-xs text-foreground-tertiary" title={entry.detail}>
						{entry.operation}: {entry.detail || entry.target}
					</p>
				</div>
			{/each}
		{/if}
	</div>
</div>
