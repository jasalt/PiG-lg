(ns pig.extension)

;; Thin wrappers over the api capability map passed to (init api).
;; Extension code depends on these names, never on the map's Go-backed values.

(defn register-tool! [api tool] ((:register-tool! api) tool))

(defn register-command! [api command] ((:register-command! api) command))

(defn on-event [api event handler] ((:on-event api) event handler))

;; Result hooks take a one-argument handler (fn [event]) and return a replacement map or nil.
(defn on-before-agent-start [api handler] ((:on-before-agent-start api) handler))

(defn on-tool-call [api handler] ((:on-tool-call api) handler))

(defn on-tool-result [api handler] ((:on-tool-result api) handler))
