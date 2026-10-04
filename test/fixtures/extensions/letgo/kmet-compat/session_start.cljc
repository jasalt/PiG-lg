(ns kmet-compat.session-start
  (:require #?(:lg [pig.extension :as ext]
               :default [kmet.extension :as ext])))

(def log (atom []))

(defn init [api]
  (ext/on-event
   api
   :session-start
   (fn [event ctx]
     (swap! log conj (str "start " (name (:reason event)))))))
