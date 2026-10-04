(ns kmet-compat.command
  (:require #?(:lg [pig.extension :as ext]
               :default [kmet.extension :as ext])))

(def log (atom []))

(defn init [api]
  (ext/register-command!
   api
   {:name "greet"
    :description "Greet someone"
    :handler (fn [ctx args]
               (swap! log conj (str "greet " args))
               nil)}))
