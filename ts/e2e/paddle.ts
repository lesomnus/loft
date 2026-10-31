import type { Page } from '@playwright/test'

/**
 * What the page told Paddle.js, in the order it told it: the environment it
 * set, the token it initialized it with, and each checkout opened -- the
 * transaction and the address Paddle is to send the payer back to.
 */
export type Told = { environment: string } | { token: string } | { open: string; successUrl: string }

/**
 * Paddle.js as the browser tests have it, never Paddle's (design 7.7): what
 * Paddle.js does with what a page tells it, and nothing of Paddle. It opens
 * the checkout of the transaction the address names (`_ptxn`) as it is
 * initialized, as Paddle.js does, with the settings the page initialized it
 * with; and a checkout opened completes at once -- `checkout.completed`, then
 * the payer sent to the success address when there is one.
 */
const script = `(function () {
	var told = (window.paddled = [])
	function tell(v) {
		told.push(v)
		console.log('paddle ' + JSON.stringify(v))
	}
	var options = {}
	function open(o) {
		var settings = Object.assign({}, (options.checkout && options.checkout.settings) || {}, o.settings || {})
		tell({ open: o.transactionId, successUrl: settings.successUrl || '' })
		var said = options.eventCallback || function () {}
		setTimeout(function () {
			said({ name: 'checkout.loaded' })
			said({ name: 'checkout.completed', data: { transaction_id: o.transactionId } })
			if (settings.successUrl) location.href = settings.successUrl
		}, 50)
	}
	window.Paddle = {
		Environment: { set: function (e) { tell({ environment: e }) } },
		Initialize: function (o) {
			options = o || {}
			tell({ token: options.token })
			var m = /[?&]_ptxn=([^&#]+)/.exec(location.search)
			if (m) open({ transactionId: decodeURIComponent(m[1]) })
		},
		Checkout: { open: open },
	}
})()
`

/**
 * Has `page` load the fake Paddle.js where it asks for Paddle's, and refuses
 * it anything else of Paddle's hosts, so that no test reaches Paddle. It
 * answers what the page has told Paddle.js so far -- on every page it opened
 * since, as the fake says it in the console -- and what else it asked Paddle
 * for, which is nothing.
 */
export async function paddle(page: Page): Promise<{ told: Told[]; asked: string[] }> {
	const told: Told[] = []
	page.on('console', (m) => {
		const v = m.text()
		if (v.startsWith('paddle ')) told.push(JSON.parse(v.slice('paddle '.length)) as Told)
	})
	const asked: string[] = []
	// The last route added is asked first: the script, then the rest.
	await page.route(/^https:\/\/([a-z0-9-]+\.)*paddle\.com\//, (route) => {
		asked.push(route.request().url())
		return route.abort('blockedbyclient')
	})
	await page.route('https://cdn.paddle.com/paddle/v2/paddle.js', (route) =>
		route.fulfill({ status: 200, contentType: 'text/javascript', body: script }),
	)
	return { told, asked }
}
