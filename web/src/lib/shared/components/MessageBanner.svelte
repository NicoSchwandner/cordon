<script lang="ts">
	let {
		message = $bindable(''),
		variant = 'error'
	}: {
		message: string;
		variant?: 'error' | 'warning' | 'info';
	} = $props();

	const styles: Record<string, string> = {
		error: 'bg-danger-bg border-danger-border text-danger-text',
		warning: 'bg-warning-bg border-warning-border text-warning-text',
		info: 'bg-info-badge-bg border-info-border text-info-text'
	};
</script>

{#if message}
	<div class="flex items-center justify-between gap-2 rounded-lg border px-4 py-3 text-sm {styles[variant]}" role="alert">
		<span>{message}</span>
		<button onclick={() => (message = '')} class="shrink-0 opacity-60 transition-opacity hover:opacity-100" aria-label="Dismiss">
			<svg class="h-4 w-4" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M4 4l8 8M12 4l-8 8"/></svg>
		</button>
	</div>
{/if}
