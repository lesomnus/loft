// The page Paddle's default payment link is (`server/checkout`): Paddle.js,
// initialized with this deployment's environment and client-side token, opens
// the checkout of the transaction the address names (`_ptxn`) by itself, and
// this says what became of it.
//
// A checkout this deployment began (the account's page of what it pays with)
// names its transaction otherwise, `transaction`, with levyan's checkout
// beside it, `checkout`: this opens it, and sends the payer back to that page
// once they finished, which asks levyan whether the subscription was made.
// The way back is a path here, made here: only the checkout's identifier is
// taken from the address, and only of the shape levyan writes one in.
//
// The environment and the token are the page's own data attributes, written
// by the server from `app.billing.checkout`; nothing here is a secret.
;(function () {
	'use strict'

	var at = document.getElementById('checkout')
	var said = document.getElementById('said')
	if (at === null) return

	function say(text) {
		if (said !== null) said.textContent = text
	}

	var paddle = window.Paddle
	if (paddle === undefined) {
		// Its script did not load: blocked, or Paddle not there just now.
		say('결제창을 불러오지 못했습니다. 잠시 뒤에 이 페이지를 다시 여세요.')
		return
	}

	if (at.dataset.environment === 'sandbox') paddle.Environment.set('sandbox')

	var q = new URLSearchParams(location.search)
	var transaction = q.get('transaction')
	var checkout = q.get('checkout')

	var done = false
	paddle.Initialize({
		token: at.dataset.token,
		eventCallback: function (e) {
			switch (e && e.name) {
				case 'checkout.completed':
					done = true
					say('결제를 마쳤습니다.')
					break
				case 'checkout.closed':
					if (!done) say(transaction === null ? '결제창을 닫았습니다. 결제하려면 받은 결제 링크를 다시 여세요.' : '결제창을 닫았습니다. 결제 화면에서 다시 시작할 수 있습니다.')
					break
				case 'checkout.error':
					say('결제창을 열 수 없습니다. 받은 결제 링크가 맞는지 확인하세요.')
					break
			}
		},
	})

	if (transaction !== null) {
		if (!/^txn_[a-z0-9]+$/.test(transaction)) {
			say('결제창을 열 수 없습니다. 결제 화면에서 다시 시작하세요.')
			return
		}
		var settings = {}
		if (checkout !== null && /^[0-9A-Za-z_-]{1,64}$/.test(checkout)) {
			settings.successUrl = location.origin + '/billing?checkout=' + encodeURIComponent(checkout)
		}
		paddle.Checkout.open({ transactionId: transaction, settings: settings })
	}
})()
