(ns pig.extension)

;; Thin wrappers over the api capability map passed to (init api).
;; Extension code depends on these names, never on the map's Go-backed values.

(defn register-tool! [api tool] ((:register-tool! api) tool))

(defn register-command! [api command] ((:register-command! api) command))

(defn on-event [api event handler] ((:on-event api) event handler))
