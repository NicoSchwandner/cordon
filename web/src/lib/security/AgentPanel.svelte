<script lang="ts">
	import type { AuditEntry } from '$lib/shared/api/types';
	import { TIER_NAMES } from '$lib/shared/api/types';
	import { connectAuditWS } from '$lib/shared/api/websocket';
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

	const DECISION_ICONS: Record<string, string> = {
		allowed: '✓',
		denied: '✗',
		blocked: '⊘',
		pending_approval: '⏳'
	};

	const DECISION_LINE_COLORS: Record<string, string> = {
		allowed: 'border-l-success-text',
		denied: 'border-l-danger-text',
		blocked: 'border-l-danger-text',
		pending_approval: 'border-l-warning-text'
	};

	function summarize(entry: AuditEntry): string {
		const op = entry.operation;
		const target = entry.target;
		const detail = entry.detail;

		if (op.startsWith('HTTP:')) {
			const method = op.replace('HTTP:', '');
			// detail is "METHOD /path", target is "host/path"
			// Extract just the path from target to avoid "GET GET /path"
			const path = target.includes('/') ? '/' + target.split('/').slice(1).join('/') : target;
			return `${method} ${path || '/'}`;
		}

		if (op.startsWith('SQL:') || op === 'SQL') {
			return detail || target;
		}

		if (detail) return `${op} → ${detail}`;
		return `${op} → ${target}`;
	}

	function timeAgo(ts: string): string {
		const diff = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
		if (diff < 5) return 'now';
		if (diff < 60) return `${diff}s ago`;
		if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
		return `${Math.floor(diff / 3600)}h ago`;
	}

	function hostFrom(entry: AuditEntry): string {
		// target is "host/path" for HTTP entries (no scheme)
		const target = entry.target;
		if (target.includes('/')) return target.split('/')[0];
		// If no slash, the whole target might be a host
		if (target.includes('.')) return target;
		return '';
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
				{@const summary = summarize(entry)}
				{@const host = hostFrom(entry)}
				{@const lineColor = DECISION_LINE_COLORS[entry.decision] ?? 'border-l-border'}
				<div class="animate-fade-in border-b border-border-subtle border-l-2 {lineColor} px-3 py-2">
					<div class="flex items-center justify-between gap-2">
						<span class="truncate font-mono text-xs font-medium text-foreground" title={entry.detail || entry.target}>
							{DECISION_ICONS[entry.decision] ?? '•'} {summary}
						</span>
						<span class="shrink-0 text-[10px] text-foreground-faint">{timeAgo(entry.timestamp)}</span>
					</div>
					<div class="mt-0.5 flex items-center gap-2">
						<DecisionBadge decision={entry.decision} />
						<span class="text-[10px] text-foreground-faint">{TIER_NAMES[entry.tier] ?? `T${entry.tier}`}</span>
						{#if host}
							<span class="text-[10px] text-foreground-faint">→ {host}</span>
						{/if}
						{#if entry.duration_ms > 0}
							<span class="text-[10px] text-foreground-faint">{entry.duration_ms}ms</span>
						{/if}
					</div>
				</div>
			{/each}
		{/if}
	</div>
</div>
