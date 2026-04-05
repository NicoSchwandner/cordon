<script lang="ts">
	let props: { expiresAt: string; label?: string } = $props();

	let now = $state(Date.now());

	$effect(() => {
		const id = setInterval(() => (now = Date.now()), 10000);
		return () => clearInterval(id);
	});

	const remaining = $derived.by(() => {
		const diff = new Date(props.expiresAt).getTime() - now;
		if (diff <= 0) return { text: 'expired', urgent: true };
		const minutes = Math.floor(diff / 60000);
		const hours = Math.floor(minutes / 60);
		const mins = minutes % 60;
		const prefix = props.label ? `${props.label} ` : '';
		if (hours > 0) return { text: `${prefix}${hours}h ${mins}m`, urgent: false };
		return { text: `${prefix}${mins}m`, urgent: mins < 30 };
	});
</script>

<time
	datetime={props.expiresAt}
	title="Expires {new Date(props.expiresAt).toLocaleString()}"
	class="whitespace-nowrap {remaining.urgent ? 'text-warning-text' : 'text-foreground-muted'}"
>
	{remaining.text}
</time>
